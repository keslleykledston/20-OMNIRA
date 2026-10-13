// Package engine is the central routing engine (ADR-0044 phases 4-5): ONE place that decides where a conversation goes. It does not
// re-implement the rules: the calendar comes from internal/scheduling (through sectors/adapters), the eligibility from
// internal/routing/eligibility, the overflow chain from internal/routing/overflow, and the assignment keeps the guarantees of the
// round-robin it replaces (compare-and-set on the owner, capacity re-checked under the same member-row lock, assignment_events).
//
// Lock order, everywhere: conversation row -> routing state -> timer. A decision is ONE transaction.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/eligibility"
	"github.com/omnira/omnira/internal/routing/overflow"
	"github.com/omnira/omnira/internal/routing/ports"
	sectorsadapters "github.com/omnira/omnira/internal/sectors/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// engineCandidateLimit bounds the people evaluated per decision. A queue with more members than this is reported (ReasonTruncated)
// instead of silently starving whoever comes after the limit; a paged candidate walk is the follow-up for queues of that size.
const engineCandidateLimit = 2000

type Kind string

const (
	AssignAgent         Kind = "ASSIGN_AGENT"
	Enqueue             Kind = "ENQUEUE"
	Overflow            Kind = "OVERFLOW"
	WaitUntilOpen       Kind = "WAIT_UNTIL_OPEN"
	ExecuteFlow         Kind = "EXECUTE_FLOW"
	Escalate            Kind = "ESCALATE"
	RequireManualAction Kind = "REQUIRE_MANUAL_ACTION"
	NoAction            Kind = "NO_ACTION" // engine-internal: nothing to route (closed conversation, no queue)
)

// Reasons are a closed set (they are stored and shown).
const (
	ReasonAlreadyAssigned   = "already_assigned"
	ReasonNoQueue           = "no_queue"
	ReasonClosed            = "conversation_closed"
	ReasonManualQueue       = "manual_queue"
	ReasonSectorClosed      = "sector_closed"
	ReasonNeverOpens        = "never_opens"
	ReasonNoEligibleAgent   = "no_eligible_agent"
	ReasonCapacityExhausted = "capacity_exhausted"
	ReasonAssigned          = "assigned"
	ReasonPresenceDown      = "presence_unavailable"
	ReasonTruncated         = "candidates_truncated"
	ReasonChainExhausted    = "chain_exhausted"
	ReasonOverflowOnCall    = "overflow_oncall"
	ReasonOverflowAgent     = "overflow_agent"
)

// Triggers (the part of the idempotency key that says WHY the engine was asked).
const (
	TriggerRoute      = "route"       // a new conversation, a retry of the routing job, the safety sweep
	TriggerOpenWakeup = "open_wakeup" // the durable timer of a sector that was closed
	TriggerStepWait   = "step_wait"   // the durable timer of a wait inside an overflow chain
)

type Request struct {
	ConversationID uuid.UUID
	Trigger        string
	StrategyReason string // written to assignment_events (kept: "round_robin" for the legacy-compatible history)
}

type Decision struct {
	Kind         Kind
	Reason       string
	Destination  string // canonical key: agent:<uuid> | queue:<uuid> | oncall:<uuid>
	AssignedUser uuid.UUID
	WakeAt       *time.Time
	Attempts     int
}

type Engine struct {
	pool     *pgxpool.Pool
	presence sectorsadapters.PresenceChecker
}

func New(pool *pgxpool.Pool, presence sectorsadapters.PresenceChecker) *Engine {
	return &Engine{pool: pool, presence: presence}
}

var ErrNotFound = errors.New("engine: conversation not found")

type convRow struct {
	queueID  *uuid.UUID
	assigned *uuid.UUID
	status   string
	mode     string
}

// stateRow is the routing state of one conversation (one row, locked for the whole decision).
type stateRow struct {
	episode     int
	step        int // 0 = still in the origin queue, n = the n-th step of the overflow chain
	hops        int
	visited     []string
	origin      *uuid.UUID
	queue       *uuid.UUID
	stepEntered time.Time
	state       string
}

// Decide runs inside the caller's system tenant session (the routing worker's), in the caller's transaction. The tenant comes from that
// context; a conversation id of another tenant simply is not found.
func (e *Engine) Decide(ctx context.Context, req Request) (Decision, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.Source != tenancydomain.AccessSourceSystem {
		return Decision{}, errors.New("engine: system tenant context required")
	}
	if req.ConversationID == uuid.Nil {
		return Decision{}, errors.New("engine: conversation required")
	}
	trigger := req.Trigger
	if trigger == "" {
		trigger = TriggerRoute
	}
	q := platformdb.QuerierFromContext(ctx, e.pool)
	tenant := tc.TenantID

	// 1. the conversation row is the serialization point: two workers for the same conversation queue up here
	var c convRow
	err = q.QueryRow(ctx, `SELECT c.queue_id, c.assigned_to_user_id, c.status, COALESCE(qq.mode, '')
		FROM conversations c LEFT JOIN queues qq ON qq.tenant_id=c.tenant_id AND qq.id=c.queue_id
		WHERE c.tenant_id=$1 AND c.id=$2 FOR UPDATE OF c`, tenant, req.ConversationID).Scan(&c.queueID, &c.assigned, &c.status, &c.mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return Decision{}, ErrNotFound
	}
	if err != nil {
		return Decision{}, err
	}
	// 2. the routing state (created on first sight; a person's action starts a new episode)
	st, err := loadState(ctx, q, tenant, req.ConversationID, c.queueID, c.status == "open" && c.assigned == nil)
	if err != nil {
		return Decision{}, err
	}
	detail := map[string]any{}
	finish := func(d Decision, state string) (Decision, error) {
		return persist(ctx, q, tenant, req.ConversationID, &st, trigger, detail, d, state)
	}
	switch {
	case c.status != "open":
		return finish(Decision{Kind: NoAction, Reason: ReasonClosed}, "done")
	case c.assigned != nil: // somebody (a person, an earlier run) already owns it: never reassign
		if err := cancelTimers(ctx, q, tenant, req.ConversationID, st.episode); err != nil {
			return Decision{}, err
		}
		return finish(Decision{Kind: AssignAgent, Reason: ReasonAlreadyAssigned, Destination: "agent:" + c.assigned.String(), AssignedUser: *c.assigned}, "assigned")
	case c.queueID == nil:
		return finish(Decision{Kind: NoAction, Reason: ReasonNoQueue}, "done")
	}
	var now time.Time
	if err := q.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return Decision{}, err
	}
	var pol *overflow.Policy
	if st.origin != nil {
		if pol, err = sectorsadapters.LoadPolicy(ctx, q, tenant, *st.origin); err != nil {
			return Decision{}, err
		}
		if pol != nil && !pol.Active {
			pol = nil
		}
	}

	// the relaxations (closed destination, off-shift agent) of the step the conversation is in: explicit, authorized when the policy was saved
	relaxOf := func() eligibility.Policy {
		if pol != nil && st.step >= 1 && st.step <= len(pol.Steps) {
			s := pol.Steps[st.step-1]
			return eligibility.Policy{AllowClosed: s.AllowClosed, AllowOffShift: s.AllowOffShift}
		}
		return eligibility.Policy{}
	}
	// 3. evaluate the queue the conversation is in; if it is blocked, the overflow chain may move it to another destination, and the new
	// place is evaluated in turn (bounded by the policy's hop limit, so this loop always ends)
	for iter := 0; iter <= overflow.MaxStepsLimit+1; iter++ {
		queue := *c.queueID
		out, err := e.evaluate(ctx, q, tenant, req, &st, queue, c.mode, now, relaxOf())
		if err != nil {
			return Decision{}, err
		}
		if out.kind == oAssigned {
			if err := cancelTimers(ctx, q, tenant, req.ConversationID, st.episode); err != nil {
				return Decision{}, err
			}
			return finish(out.plain, "assigned")
		}
		if out.kind == oNever { // the calendar never opens within the horizon: a person decides (the chain cannot help a closed door)
			if err := cancelTimers(ctx, q, tenant, req.ConversationID, st.episode); err != nil {
				return Decision{}, err
			}
			return finish(out.plain, "manual_required")
		}
		// blocked (or a manual queue): consult the chain
		var res chainResult
		if pol != nil && !out.presenceDown { // a presence backend that is down is an infrastructure fault, not a business trigger: the chain does not act on it
			if res, err = e.chain(ctx, q, tenant, req, &st, pol, out.trigger, now); err != nil {
				return Decision{}, err
			}
		}
		if out.trigger != "" {
			detail["blocking"] = string(out.trigger)
		}
		if len(res.rejected) > 0 { // destinations that were looked at and refused, with the reason (the audit trail of a chain that skipped)
			detail["rejected"] = res.rejected
		}
		switch res.kind {
		case cEntered: // moved to another queue: evaluate it
			c.queueID = st.queue
			c.mode = res.mode
			continue
		case cAssigned:
			if err := cancelTimers(ctx, q, tenant, req.ConversationID, st.episode); err != nil {
				return Decision{}, err
			}
			return finish(res.decision, "assigned")
		case cExhausted:
			if err := cancelTimers(ctx, q, tenant, req.ConversationID, st.episode); err != nil {
				return Decision{}, err
			}
			return finish(res.decision, res.state)
		}
		// the chain did not move it: stay here with the plain decision, and keep the durable timers it needs
		if out.wake != nil {
			if err := planTimer(ctx, q, tenant, req.ConversationID, st.episode, "open_wakeup", 0, *out.wake); err != nil {
				return Decision{}, err
			}
		}
		for _, w := range res.waits {
			if err := planTimer(ctx, q, tenant, req.ConversationID, st.episode, "step_wait", w.position, w.due); err != nil {
				return Decision{}, err
			}
		}
		d, err := finish(out.plain, out.state)
		if err != nil {
			return Decision{}, err
		}
		if out.presenceDown { // the legacy contract: a presence backend that cannot answer is retried, never folded into "nobody available"
			return d, ports.ErrPresenceUnavailable
		}
		return d, nil
	}
	return Decision{}, errors.New("engine: routing did not settle (hop limit)")
}

type outKind int

const (
	oAssigned outKind = iota
	oBlocked
	oNever
)

type outcome struct {
	kind         outKind
	trigger      overflow.Trigger // what blocks it ("" for a manual queue: only max_wait can move it)
	plain        Decision         // what to report if the chain does not move it
	state        string           // the routing state that goes with `plain`
	wake         *time.Time       // the open-wakeup timer to keep if it stays
	presenceDown bool
}

// evaluate looks at the queue the conversation is in: manual, closed, or open (then it tries the eligible agents in the strategy's order).
func (e *Engine) evaluate(ctx context.Context, q platformdb.Querier, tenant uuid.UUID, req Request, st *stateRow, queue uuid.UUID, mode string, now time.Time, relax eligibility.Policy) (outcome, error) {
	if mode == "manual" || mode == "" {
		return outcome{kind: oBlocked, plain: Decision{Kind: Enqueue, Reason: ReasonManualQueue, Destination: "queue:" + queue.String()}, state: "queued"}, nil
	}
	avail, err := sectorsadapters.QueueAvailability(ctx, q, tenant, queue, now)
	if err != nil {
		return outcome{}, err
	}
	if !avail.Open && !relax.AllowClosed {
		if avail.NextOpen == nil {
			return outcome{kind: oNever, plain: Decision{Kind: RequireManualAction, Reason: ReasonNeverOpens, Destination: "queue:" + queue.String()}}, nil
		}
		return outcome{kind: oBlocked, trigger: overflow.AfterHours, wake: avail.NextOpen, state: "waiting_open",
			plain: Decision{Kind: WaitUntilOpen, Reason: ReasonSectorClosed, Destination: "queue:" + queue.String(), WakeAt: avail.NextOpen}}, nil
	}
	snap, err := sectorsadapters.SnapshotWith(ctx, q, tenant, queue, now, e.presence, sectorsadapters.SnapshotOptions{Limit: engineCandidateLimit, Policy: relax})
	if err != nil {
		return outcome{}, err
	}
	anyPresenceDown := false
	for _, a := range snap.Agents {
		for _, r := range a.Reasons {
			if r == eligibility.PresenceUnavailable {
				anyPresenceDown = true
			}
		}
	}
	for _, cand := range order(snap.Agents, mode) {
		ok, err := tryAssign(ctx, q, tenant, queue, req.ConversationID, cand, req.StrategyReason, now, relax)
		if err != nil {
			return outcome{}, err
		}
		if ok {
			return outcome{kind: oAssigned, plain: Decision{Kind: AssignAgent, Reason: ReasonAssigned, Destination: "agent:" + cand.UserID.String(), AssignedUser: cand.UserID}}, nil
		}
		// lost a race (capacity, lock, or the conversation was taken meanwhile): look at the conversation again before the next candidate
		var owner *uuid.UUID
		if err := q.QueryRow(ctx, `SELECT assigned_to_user_id FROM conversations WHERE tenant_id=$1 AND id=$2`, tenant, req.ConversationID).Scan(&owner); err != nil {
			return outcome{}, err
		}
		if owner != nil {
			return outcome{kind: oAssigned, plain: Decision{Kind: AssignAgent, Reason: ReasonAlreadyAssigned, Destination: "agent:" + owner.String(), AssignedUser: *owner}}, nil
		}
	}
	reason, trig := ReasonNoEligibleAgent, overflow.NoEligibleAgent
	switch {
	case snap.Truncated:
		reason = ReasonTruncated
	case anyPresenceDown:
		reason = ReasonPresenceDown
	case onlyCapacity(snap.Agents):
		reason, trig = ReasonCapacityExhausted, overflow.CapacityExhausted
	}
	return outcome{kind: oBlocked, trigger: trig, state: "queued", presenceDown: anyPresenceDown,
		plain: Decision{Kind: Enqueue, Reason: reason, Destination: "queue:" + queue.String()}}, nil
}

type chainKind int

const (
	cNone chainKind = iota
	cEntered
	cAssigned
	cExhausted
)

type stepWait struct {
	position int
	due      time.Time
}

// rejection records why a candidate destination was not entered; it goes into the decision's detail (the audit trail).
type rejection struct {
	Position int    `json:"position"`
	Reason   string `json:"reason"`
}

type chainResult struct {
	rejected []rejection
	kind     chainKind
	mode     string // the mode of the queue it moved to
	decision Decision
	state    string
	waits    []stepWait
}

// chain consults the overflow policy for the conversation's situation. Triggers evaluated: the one that blocks it now, and max_wait
// (the time in the current step). It performs AT MOST ONE transition per call; an immediate next step happens in the caller's loop.
func (e *Engine) chain(ctx context.Context, q platformdb.Querier, tenant uuid.UUID, req Request, st *stateRow, pol *overflow.Policy, blocking overflow.Trigger, now time.Time) (chainResult, error) {
	origin := *st.origin
	visited := map[string]bool{}
	for _, k := range st.visited {
		visited[k] = true
	}
	triggers := []overflow.Trigger{}
	if blocking != "" {
		triggers = append(triggers, blocking)
	}
	triggers = append(triggers, overflow.MaxWait)
	elapsedFn := func(business bool) time.Duration {
		d, err := sectorsadapters.Elapsed(ctx, q, tenant, origin, st.stepEntered, now, business)
		if err != nil {
			return 0 // an unreadable calendar must not make a wait look elapsed
		}
		return d
	}
	var res chainResult
	for attempt := 0; attempt <= len(pol.Steps); attempt++ {
		var enter *overflow.Step
		waits := map[int]*overflow.Step{}
		exhausted := false
		for _, t := range triggers {
			sel := pol.Next(st.step, t, visited, st.hops, elapsedFn)
			switch {
			case sel.Step != nil:
				if enter == nil || sel.Step.Position < enter.Position {
					enter = sel.Step
				}
			case sel.WaitFor != nil:
				waits[sel.WaitFor.Position] = sel.WaitFor
			case sel.Exhausted:
				exhausted = true
			}
		}
		if enter != nil { // steps are ordered: a later step never jumps ahead of an earlier one that is still waiting (another trigger's wait)
			for pos := range waits {
				if pos < enter.Position {
					enter = nil
					break
				}
			}
		}
		if enter != nil {
			entered, a, why, err := e.enter(ctx, q, tenant, req, st, *enter, blocking, now)
			if err != nil {
				return chainResult{}, err
			}
			switch entered {
			case entRejected: // the live check refused this destination: never retry it in this call, look at the next candidate, and say why
				visited[enter.Key()] = true
				res.rejected = append(res.rejected, rejection{Position: enter.Position, Reason: why})
				continue
			case entQueue:
				return chainResult{kind: cEntered, mode: a.mode, rejected: res.rejected}, nil
			case entAssigned:
				return chainResult{kind: cAssigned, decision: a.decision, rejected: res.rejected}, nil
			}
		}
		for pos, s := range waits {
			due, err := sectorsadapters.DueAfter(ctx, q, tenant, origin, st.stepEntered, time.Duration(s.WaitSeconds)*time.Second, s.WaitBusiness)
			if err != nil {
				return chainResult{}, err
			}
			res.waits = append(res.waits, stepWait{position: pos, due: due})
		}
		sort.Slice(res.waits, func(i, j int) bool { return res.waits[i].position < res.waits[j].position })
		if len(res.waits) == 0 && (exhausted || len(visited) > len(st.visited)) { // every candidate was visited, over the limit, or rejected
			d, state := exhaustedDecision(pol, "queue:"+st.queue.String())
			return chainResult{kind: cExhausted, decision: d, state: state, rejected: res.rejected}, nil
		}
		return res, nil
	}
	d, state := exhaustedDecision(pol, "queue:"+st.queue.String())
	return chainResult{kind: cExhausted, decision: d, state: state, rejected: res.rejected}, nil
}

func exhaustedDecision(pol *overflow.Policy, dest string) (Decision, string) {
	switch pol.OnExhausted {
	case overflow.ExhaustedContingency:
		return Decision{Kind: Escalate, Reason: ReasonChainExhausted, Destination: dest}, "manual_required"
	case overflow.ExhaustedManual:
		return Decision{Kind: RequireManualAction, Reason: ReasonChainExhausted, Destination: dest}, "manual_required"
	}
	return Decision{Kind: Enqueue, Reason: ReasonChainExhausted, Destination: dest}, "queued"
}

type entryKind int

const (
	entRejected entryKind = iota
	entQueue
	entAssigned
)

type entryResult struct {
	mode     string
	decision Decision
}

// enter performs the transition to one step after checking the destination LIVE (it may have changed since the policy was saved).
// It returns why a destination was refused, so the history can say it.
func (e *Engine) enter(ctx context.Context, q platformdb.Querier, tenant uuid.UUID, req Request, st *stateRow, s overflow.Step, blocking overflow.Trigger, now time.Time) (entryKind, entryResult, string, error) {
	label := string(blocking)
	if label == "" {
		label = string(overflow.MaxWait)
	}
	visited := map[string]bool{}
	for _, k := range st.visited {
		visited[k] = true
	}
	switch s.DestKind {
	case overflow.DestQueue, overflow.DestSector, overflow.DestHub:
		var dest uuid.UUID
		var extra []string
		switch s.DestKind {
		case overflow.DestQueue:
			dest = s.DestID
		case overflow.DestHub:
			dest = *s.DestQueueID
			if visited[overflow.DestKey(overflow.DestQueue, dest)] {
				return entRejected, entryResult{}, overflow.SkipVisited, nil
			}
			var ready bool
			if err := q.QueryRow(ctx, `SELECT hub_destination_ready($1, $2, $3)`, tenant, s.DestID, dest).Scan(&ready); err != nil {
				return entRejected, entryResult{}, "", err
			}
			if !ready { // contract not active/valid, queue outside its scope, tenant or Hub inactive, or no live operator who may reply
				return entRejected, entryResult{}, "hub_not_ready", nil
			}
			extra = append(extra, overflow.DestKey(overflow.DestQueue, dest))
		case overflow.DestSector:
			chosen, why, err := e.chooseSectorQueue(ctx, q, tenant, s, visited, now)
			if err != nil {
				return entRejected, entryResult{}, "", err
			}
			if chosen == uuid.Nil {
				return entRejected, entryResult{}, why, nil
			}
			dest = chosen
			extra = append(extra, overflow.DestKey(overflow.DestQueue, dest))
		}
		var mode string
		var sectorActive bool
		err := q.QueryRow(ctx, `SELECT qq.mode, COALESCE(sc.active, true) FROM queues qq LEFT JOIN sectors sc ON sc.tenant_id=qq.tenant_id AND sc.id=qq.sector_id
			WHERE qq.tenant_id=$1 AND qq.id=$2`, tenant, dest).Scan(&mode, &sectorActive)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !sectorActive) {
			return entRejected, entryResult{}, "destination_unavailable", nil // the queue is gone, or its sector was deactivated
		}
		if err != nil {
			return entRejected, entryResult{}, "", err
		}
		avail, err := sectorsadapters.QueueAvailability(ctx, q, tenant, dest, now)
		if err != nil {
			return entRejected, entryResult{}, "", err
		}
		if !avail.Open && !s.AllowClosed {
			return entRejected, entryResult{}, "destination_closed", nil
		}
		if _, err := q.Exec(ctx, `UPDATE conversations SET queue_id=$3, updated_at=now(),
			routing_retry_at = CASE WHEN $4 IN ('round_robin','least_loaded') THEN now() + interval '90 seconds' ELSE NULL END
			WHERE tenant_id=$1 AND id=$2 AND assigned_to_user_id IS NULL AND status='open'`, tenant, req.ConversationID, dest, mode); err != nil {
			return entRejected, entryResult{}, "", err
		}
		if err := cancelTimers(ctx, q, tenant, req.ConversationID, st.episode); err != nil {
			return entRejected, entryResult{}, "", err
		}
		advance(st, s, &dest, now)
		st.visited = append(st.visited, extra...)
		if _, err := persist(ctx, q, tenant, req.ConversationID, st, req.triggerOrRoute()+":overflow", map[string]any{"blocking": label, "queue": dest.String()},
			Decision{Kind: Overflow, Reason: label, Destination: s.Key()}, "queued"); err != nil {
			return entRejected, entryResult{}, "", err
		}
		return entQueue, entryResult{mode: mode}, "", nil
	case overflow.DestOnCall, overflow.DestAgent:
		var user uuid.UUID
		var ok bool
		var err error
		if s.DestKind == overflow.DestOnCall {
			user, ok, err = e.pickOnCall(ctx, q, tenant, req, st, s, now)
		} else {
			user, ok, err = e.pickAgent(ctx, q, tenant, req, st, s, now)
		}
		if err != nil {
			return entRejected, entryResult{}, "", err
		}
		if !ok {
			return entRejected, entryResult{}, "no_eligible_" + s.DestKind, nil
		}
		advance(st, s, nil, now)
		reason := ReasonOverflowOnCall
		if s.DestKind == overflow.DestAgent {
			reason = ReasonOverflowAgent
		}
		if _, err := persist(ctx, q, tenant, req.ConversationID, st, req.triggerOrRoute()+":overflow", map[string]any{"blocking": label},
			Decision{Kind: Overflow, Reason: label, Destination: s.Key()}, "queued"); err != nil {
			return entRejected, entryResult{}, "", err
		}
		return entAssigned, entryResult{decision: Decision{Kind: AssignAgent, Reason: reason, Destination: "agent:" + user.String(), AssignedUser: user}}, "", nil
	}
	return entRejected, entryResult{}, "unsupported_destination", nil
}

// chooseSectorQueue picks, among the queues of the sector (by name), the first that has not been visited, is open (or the step allows a
// closed one) and has at least one eligible agent; when none has an agent, the first acceptable queue (the conversation waits there).
func (e *Engine) chooseSectorQueue(ctx context.Context, q platformdb.Querier, tenant uuid.UUID, s overflow.Step, visited map[string]bool, now time.Time) (uuid.UUID, string, error) {
	rows, err := q.Query(ctx, `SELECT qq.id, qq.mode FROM queues qq JOIN sectors sc ON sc.tenant_id=qq.tenant_id AND sc.id=qq.sector_id
		WHERE qq.tenant_id=$1 AND qq.sector_id=$2 AND sc.active ORDER BY qq.name, qq.id LIMIT 50`, tenant, s.DestID)
	if err != nil {
		return uuid.Nil, "", err
	}
	type cand struct {
		id   uuid.UUID
		mode string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.mode); err != nil {
			rows.Close()
			return uuid.Nil, "", err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if rows.Err() != nil {
		return uuid.Nil, "", rows.Err()
	}
	first := uuid.Nil
	for _, c := range cands {
		if visited[overflow.DestKey(overflow.DestQueue, c.id)] {
			continue
		}
		avail, err := sectorsadapters.QueueAvailability(ctx, q, tenant, c.id, now)
		if err != nil {
			return uuid.Nil, "", err
		}
		if !avail.Open && !s.AllowClosed {
			continue
		}
		if first == uuid.Nil {
			first = c.id
		}
		if c.mode == "manual" {
			continue
		}
		snap, err := sectorsadapters.SnapshotWith(ctx, q, tenant, c.id, now, e.presence, sectorsadapters.SnapshotOptions{Limit: engineCandidateLimit,
			Policy: eligibility.Policy{AllowClosed: s.AllowClosed, AllowOffShift: s.AllowOffShift}})
		if err != nil {
			return uuid.Nil, "", err
		}
		if snap.EligibleCnt > 0 {
			return c.id, "", nil
		}
	}
	if first == uuid.Nil {
		return uuid.Nil, "sector_has_no_open_queue", nil
	}
	return first, "", nil
}

const defaultAgentCapacity = 5

// pickAgent assigns to a named attendant (an escalation target). Capacity: the largest capacity the person has in any queue, or
// defaultAgentCapacity, counted across the company; shift unless the step allows otherwise; presence when required. The person is
// re-read under the per-agent lock, like every other assignment.
func (e *Engine) pickAgent(ctx context.Context, q platformdb.Querier, tenant uuid.UUID, req Request, st *stateRow, s overflow.Step, now time.Time) (uuid.UUID, bool, error) {
	user := s.DestID
	var locked bool
	if err := q.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('routing:agent:' || $1::text || ':' || $2::text, 0))`, tenant, user).Scan(&locked); err != nil {
		return uuid.Nil, false, err
	}
	if !locked {
		return uuid.Nil, false, nil
	}
	var profile uuid.UUID
	var capacity, load int
	err := q.QueryRow(ctx, `SELECT ap.id, COALESCE((SELECT max(qm.capacity) FROM queue_members qm WHERE qm.tenant_id=m.tenant_id AND qm.user_id=m.user_id AND qm.active), $3::int),
		(SELECT count(*) FROM conversations c WHERE c.tenant_id=m.tenant_id AND c.assigned_to_user_id=m.user_id AND c.status='open')
		FROM memberships m JOIN agent_profiles ap ON ap.tenant_id=m.tenant_id AND ap.membership_id=m.id AND ap.status='active'
		WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active'`, tenant, user, defaultAgentCapacity).Scan(&profile, &capacity, &load)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	if load >= capacity {
		return uuid.Nil, false, nil
	}
	if open, _, err := sectorsadapters.AgentShift(ctx, q, tenant, user, *st.queue, now); err != nil {
		return uuid.Nil, false, err
	} else if open != nil && !*open && !s.AllowOffShift {
		return uuid.Nil, false, nil
	}
	var presenceReq bool
	if err := q.QueryRow(ctx, `SELECT routing_require_presence FROM tenants WHERE id=$1`, tenant).Scan(&presenceReq); err != nil {
		return uuid.Nil, false, err
	}
	if presenceReq {
		if e.presence == nil {
			return uuid.Nil, false, nil
		}
		got, err := e.presence.OnlineMembers(ctx, tenant, []uuid.UUID{profile})
		if err != nil || !got[profile] {
			return uuid.Nil, false, nil
		}
	}
	tag, err := q.Exec(ctx, `UPDATE conversations SET assigned_to_user_id=$3, assigned_at=now(), updated_at=now()
		WHERE tenant_id=$1 AND id=$2 AND assigned_to_user_id IS NULL AND status='open'`, tenant, req.ConversationID, user)
	if err != nil {
		return uuid.Nil, false, err
	}
	if tag.RowsAffected() != 1 {
		return uuid.Nil, false, nil
	}
	if _, err := q.Exec(ctx, `INSERT INTO assignment_events(tenant_id,conversation_id,from_user_id,to_user_id,changed_by,reason,actor_source) VALUES ($1,$2,NULL,$3,NULL,'overflow_agent','system')`,
		tenant, req.ConversationID, user); err != nil {
		return uuid.Nil, false, err
	}
	return user, true, nil
}

func (r Request) triggerOrRoute() string {
	if r.Trigger == "" {
		return TriggerRoute
	}
	return r.Trigger
}

// advance moves the in-memory state to the step (the caller persists it).
func advance(st *stateRow, s overflow.Step, queue *uuid.UUID, now time.Time) {
	st.step = s.Position
	st.hops++
	st.visited = append(st.visited, s.Key())
	st.stepEntered = now
	if queue != nil {
		st.queue = queue
	}
}

// pickOnCall chooses, among the people on call in the roster right now, the least loaded who is an active attendant and under their
// capacity (counted across the company: an on-call person is not tied to the queue), and online when the company requires presence.
// An on-call period IS the person's shift (as in the eligibility rule), so the personal shift does not apply to them. The candidate that
// looks best is re-read UNDER the per-agent lock before anything is written: the period, the membership and profile, the presence and the
// capacity are all checked again, because the list that chose it may be seconds old.
func (e *Engine) pickOnCall(ctx context.Context, q platformdb.Querier, tenant uuid.UUID, req Request, st *stateRow, s overflow.Step, now time.Time) (uuid.UUID, bool, error) {
	var presenceReq bool
	if err := q.QueryRow(ctx, `SELECT routing_require_presence FROM tenants WHERE id=$1`, tenant).Scan(&presenceReq); err != nil {
		return uuid.Nil, false, err
	}
	const current = `SELECT ap.id, max(p.capacity),
		(SELECT count(*) FROM conversations c WHERE c.tenant_id=p.tenant_id AND c.assigned_to_user_id=p.user_id AND c.status='open')
		FROM on_call_periods p JOIN on_call_rosters r ON r.tenant_id=p.tenant_id AND r.id=p.roster_id AND r.active
		JOIN memberships m ON m.tenant_id=p.tenant_id AND m.user_id=p.user_id AND m.status='active'
		JOIN agent_profiles ap ON ap.tenant_id=m.tenant_id AND ap.membership_id=m.id AND ap.status='active'
		WHERE p.tenant_id=$1 AND p.roster_id=$2 AND p.user_id=$3 AND p.starts_at <= $4 AND p.ends_at > $4
		GROUP BY ap.id, p.tenant_id, p.user_id`
	rows, err := q.Query(ctx, `SELECT DISTINCT p.user_id,
		(SELECT count(*) FROM conversations c WHERE c.tenant_id=p.tenant_id AND c.assigned_to_user_id=p.user_id AND c.status='open') AS load
		FROM on_call_periods p JOIN on_call_rosters r ON r.tenant_id=p.tenant_id AND r.id=p.roster_id AND r.active
		WHERE p.tenant_id=$1 AND p.roster_id=$2 AND p.starts_at <= $3 AND p.ends_at > $3
		ORDER BY load, p.user_id LIMIT 200`, tenant, s.DestID, now)
	if err != nil {
		return uuid.Nil, false, err
	}
	var users []uuid.UUID
	for rows.Next() {
		var u uuid.UUID
		var load int
		if err := rows.Scan(&u, &load); err != nil {
			rows.Close()
			return uuid.Nil, false, err
		}
		users = append(users, u)
	}
	rows.Close()
	if rows.Err() != nil {
		return uuid.Nil, false, rows.Err()
	}
	for _, user := range users {
		var locked bool
		if err := q.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('routing:agent:' || $1::text || ':' || $2::text, 0))`, tenant, user).Scan(&locked); err != nil {
			return uuid.Nil, false, err
		}
		if !locked {
			continue
		}
		// re-read under the lock
		var profile uuid.UUID
		var capacity, load int
		if err := q.QueryRow(ctx, current, tenant, s.DestID, user, now).Scan(&profile, &capacity, &load); err != nil {
			if errors.Is(err, pgx.ErrNoRows) { // left the roster, or lost the membership/profile, since the list was read
				continue
			}
			return uuid.Nil, false, err
		}
		if load >= capacity {
			continue
		}
		if presenceReq {
			if e.presence == nil {
				return uuid.Nil, false, nil // fail closed: never assume online
			}
			got, err := e.presence.OnlineMembers(ctx, tenant, []uuid.UUID{profile})
			if err != nil {
				return uuid.Nil, false, nil
			}
			if !got[profile] {
				continue
			}
		}
		tag, err := q.Exec(ctx, `UPDATE conversations SET assigned_to_user_id=$3, assigned_at=now(), updated_at=now()
			WHERE tenant_id=$1 AND id=$2 AND assigned_to_user_id IS NULL AND status='open'`, tenant, req.ConversationID, user)
		if err != nil {
			return uuid.Nil, false, err
		}
		if tag.RowsAffected() != 1 {
			return uuid.Nil, false, nil
		}
		if _, err := q.Exec(ctx, `INSERT INTO assignment_events(tenant_id,conversation_id,from_user_id,to_user_id,changed_by,reason,actor_source) VALUES ($1,$2,NULL,$3,NULL,'overflow_oncall','system')`,
			tenant, req.ConversationID, user); err != nil {
			return uuid.Nil, false, err
		}
		return user, true, nil
	}
	return uuid.Nil, false, nil
}

// order puts the eligible agents in the order of the queue's strategy. Both strategies end in user id, so the order is total.
func order(agents []sectorsadapters.AgentVerdict, mode string) []sectorsadapters.AgentVerdict {
	var out []sectorsadapters.AgentVerdict
	for _, a := range agents {
		if a.Eligible {
			out = append(out, a)
		}
	}
	older := func(a, b *time.Time) (less, decided bool) { // nil (never assigned) first
		switch {
		case a == nil && b != nil:
			return true, true
		case a != nil && b == nil:
			return false, true
		case a != nil && b != nil && !a.Equal(*b):
			return a.Before(*b), true
		}
		return false, false
	}
	sort.SliceStable(out, func(i, j int) bool {
		x, y := out[i], out[j]
		if mode == "least_loaded" && x.Workload != y.Workload {
			return x.Workload < y.Workload
		}
		if l, d := older(x.LastAssignedAt, y.LastAssignedAt); d {
			return l
		}
		return x.UserID.String() < y.UserID.String()
	})
	return out
}

func onlyCapacity(agents []sectorsadapters.AgentVerdict) bool {
	any := false
	for _, a := range agents {
		if a.Eligible {
			return false
		}
		if len(a.Reasons) == 1 && a.Reasons[0] == eligibility.NoCapacity {
			any = true
		}
	}
	return any
}

// tryAssign is the atomic step for ONE candidate. Lock order everywhere is conversation -> routing state -> timer, and here the
// conversation row is already ours; then the member row (the SAME lock the legacy round-robin takes, so a legacy worker and the engine
// can never both take the last slot of an agent), then a try-lock per agent (never wait: a busy agent is skipped). Under those locks
// the candidate is evaluated AGAIN by the same eligibility rule (membership, availability, profile, shift, on-call, capacity), because
// the snapshot that chose it may be seconds old; only then is the owner set, and only if still empty.
func tryAssign(ctx context.Context, q platformdb.Querier, tenant, queue, conversation uuid.UUID, cand sectorsadapters.AgentVerdict, reason string, now time.Time, relax eligibility.Policy) (bool, error) {
	if reason == "" {
		reason = "round_robin"
	}
	var member int
	err := q.QueryRow(ctx, `SELECT 1 FROM queue_members WHERE tenant_id=$1 AND queue_id=$2 AND user_id=$3 FOR UPDATE`, tenant, queue, cand.UserID).Scan(&member)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) { // no row = an on-call person who is not a member of the queue
		return false, err
	}
	var locked bool
	if err := q.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('routing:agent:' || $1::text || ':' || $2::text, 0))`, tenant, cand.UserID).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	only := cand.UserID
	fresh, err := sectorsadapters.SnapshotWith(ctx, q, tenant, queue, now, nil, sectorsadapters.SnapshotOptions{Only: &only, SkipPresence: true, Limit: 1, Policy: relax})
	if err != nil {
		return false, err
	}
	if len(fresh.Agents) != 1 || !fresh.Agents[0].Eligible {
		return false, nil
	}
	tag, err := q.Exec(ctx, `UPDATE conversations SET assigned_to_user_id=$3, assigned_at=now(), updated_at=now()
		WHERE tenant_id=$1 AND id=$2 AND assigned_to_user_id IS NULL AND status='open'`, tenant, conversation, cand.UserID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	if _, err := q.Exec(ctx, `UPDATE queue_members SET last_assigned_at=now() WHERE tenant_id=$1 AND queue_id=$2 AND user_id=$3`, tenant, queue, cand.UserID); err != nil {
		return false, err
	}
	if _, err := q.Exec(ctx, `INSERT INTO assignment_events(tenant_id,conversation_id,from_user_id,to_user_id,changed_by,reason,actor_source) VALUES ($1,$2,NULL,$3,NULL,$4,'system')`,
		tenant, conversation, cand.UserID, reason); err != nil {
		return false, err
	}
	return true, nil
}

// loadState reads (creating on first sight) the routing state and locks it. A NEW EPISODE starts, with an empty loop guard, only when a
// person acted: the conversation went back to the queue after having been assigned or finished (a release/reopen), or somebody moved it
// to a different queue than the one the engine left it in. Automatic requeue never resets the guard.
func loadState(ctx context.Context, q platformdb.Querier, tenant, conversation uuid.UUID, queue *uuid.UUID, openAndUnassigned bool) (stateRow, error) {
	originKey := []string{}
	if queue != nil {
		originKey = []string{overflow.DestKey(overflow.DestQueue, *queue)}
	}
	if _, err := q.Exec(ctx, `INSERT INTO routing_engine_state (tenant_id, conversation_id, state, queue_id, origin_queue_id, visited) VALUES ($1,$2,'queued',$3,$3,$4)
		ON CONFLICT (tenant_id, conversation_id) DO NOTHING`, tenant, conversation, queue, originKey); err != nil {
		return stateRow{}, fmt.Errorf("engine: routing state: %w", err)
	}
	var st stateRow
	if err := q.QueryRow(ctx, `SELECT episode, step_index, hops, visited, origin_queue_id, queue_id, step_entered_at, state FROM routing_engine_state
		WHERE tenant_id=$1 AND conversation_id=$2 FOR UPDATE`, tenant, conversation).
		Scan(&st.episode, &st.step, &st.hops, &st.visited, &st.origin, &st.queue, &st.stepEntered, &st.state); err != nil {
		return stateRow{}, err
	}
	moved := queue != nil && (st.queue == nil || *st.queue != *queue)
	reopened := openAndUnassigned && (st.state == "assigned" || st.state == "done")
	if moved || reopened {
		if err := q.QueryRow(ctx, `UPDATE routing_engine_state SET episode=episode+1, step_index=0, hops=0, visited=$3, origin_queue_id=$4, queue_id=$4,
			state='queued', entered_queue_at=now(), step_entered_at=now(), generation=generation+1, updated_at=now()
			WHERE tenant_id=$1 AND conversation_id=$2 RETURNING episode, step_entered_at`, tenant, conversation, originKey, queue).Scan(&st.episode, &st.stepEntered); err != nil {
			return stateRow{}, err
		}
		st.step, st.hops, st.visited, st.origin, st.queue, st.state = 0, 0, originKey, queue, queue, "queued"
	}
	return st, nil
}

// persist writes the decision (one row per episode, step and trigger: a repeat updates it and counts the attempt) and the state.
func persist(ctx context.Context, q platformdb.Querier, tenant, conversation uuid.UUID, st *stateRow, trigger string, detail map[string]any, d Decision, state string) (Decision, error) {
	raw, err := json.Marshal(detail)
	if err != nil || detail == nil {
		raw = []byte(`{}`)
	}
	var attempts int
	if err := q.QueryRow(ctx, `INSERT INTO routing_engine_decisions (tenant_id, conversation_id, episode, step_index, trigger, decision, destination, reason, detail)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)
		ON CONFLICT (tenant_id, conversation_id, episode, step_index, trigger)
		DO UPDATE SET
		  -- the original assignment is the decision of record: a duplicate trigger only counts as another attempt
		  decision=CASE WHEN routing_engine_decisions.decision='ASSIGN_AGENT' AND routing_engine_decisions.reason IN ('assigned','overflow_oncall','overflow_agent') THEN routing_engine_decisions.decision ELSE EXCLUDED.decision END,
		  destination=CASE WHEN routing_engine_decisions.decision='ASSIGN_AGENT' AND routing_engine_decisions.reason IN ('assigned','overflow_oncall','overflow_agent') THEN routing_engine_decisions.destination ELSE EXCLUDED.destination END,
		  reason=CASE WHEN routing_engine_decisions.decision='ASSIGN_AGENT' AND routing_engine_decisions.reason IN ('assigned','overflow_oncall','overflow_agent') THEN routing_engine_decisions.reason ELSE EXCLUDED.reason END,
		  attempts=routing_engine_decisions.attempts+1, updated_at=now()
		RETURNING attempts`, tenant, conversation, st.episode, st.step, trigger, string(d.Kind), d.Destination, d.Reason, string(raw)).Scan(&attempts); err != nil {
		return Decision{}, fmt.Errorf("engine: decision: %w", err)
	}
	if _, err := q.Exec(ctx, `UPDATE routing_engine_state SET state=$3, last_decision=$4, queue_id=$5, step_index=$6, hops=$7, visited=$8, step_entered_at=$9, updated_at=now()
		WHERE tenant_id=$1 AND conversation_id=$2`, tenant, conversation, state, string(d.Kind), st.queue, st.step, st.hops, st.visited, st.stepEntered); err != nil {
		return Decision{}, err
	}
	st.state = state
	d.Attempts = attempts
	return d, nil
}

// planTimer keeps ONE timer per (conversation, episode, kind, step): planning again moves it. A moved due time bumps the generation and
// drops the claim, so a worker that claimed the old plan cannot complete the new one.
func planTimer(ctx context.Context, q platformdb.Querier, tenant, conversation uuid.UUID, episode int, kind string, step int, due time.Time) error {
	_, err := q.Exec(ctx, `INSERT INTO routing_engine_timers (tenant_id, conversation_id, episode, kind, step_index, due_at) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (tenant_id, conversation_id, episode, kind, step_index)
		DO UPDATE SET due_at=EXCLUDED.due_at, state='pending', lease_until=NULL, claim_token=NULL,
		  generation=CASE WHEN routing_engine_timers.due_at IS DISTINCT FROM EXCLUDED.due_at THEN routing_engine_timers.generation+1 ELSE routing_engine_timers.generation END,
		  updated_at=now()`, tenant, conversation, episode, kind, step, due)
	return err
}

// cancelTimers ends every open timer of the episode (the conversation was assigned, finished or handed to a person).
func cancelTimers(ctx context.Context, q platformdb.Querier, tenant, conversation uuid.UUID, episode int) error {
	_, err := q.Exec(ctx, `UPDATE routing_engine_timers SET state='cancelled', claim_token=NULL, updated_at=now() WHERE tenant_id=$1 AND conversation_id=$2 AND episode=$3 AND state IN ('pending','leased')`, tenant, conversation, episode)
	return err
}
