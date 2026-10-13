// Package engine is the central routing engine (ADR-0044 phase 4): ONE place that decides where a conversation goes. It does not
// re-implement the rules: the calendar comes from internal/scheduling (through sectors/adapters), the eligibility from
// internal/routing/eligibility, and the assignment keeps the guarantees of the round-robin it replaces (compare-and-set on the owner,
// capacity re-checked under a per-agent lock, assignment_events).
//
// Phase 4 decides ASSIGN_AGENT, ENQUEUE, WAIT_UNTIL_OPEN and REQUIRE_MANUAL_ACTION. The other kinds (OVERFLOW, EXECUTE_FLOW,
// ESCALATE) exist in the closed set so the history is already expressive; the chain that produces them is phase 5.
package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/eligibility"
	"github.com/omnira/omnira/internal/routing/ports"
	sectorsadapters "github.com/omnira/omnira/internal/sectors/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

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
)

// Triggers (the part of the idempotency key that says WHY the engine was asked).
const (
	TriggerRoute      = "route"       // a new conversation, a retry of the routing job, the safety sweep
	TriggerOpenWakeup = "open_wakeup" // the durable timer of a sector that was closed
)

type Request struct {
	ConversationID uuid.UUID
	Trigger        string
	StrategyReason string // written to assignment_events (kept: "round_robin" for the legacy-compatible history)
}

type Decision struct {
	Kind         Kind
	Reason       string
	Destination  string // canonical key: agent:<uuid> | queue:<uuid>
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
	// 2. the routing state of the conversation (created on first sight)
	episode, err := ensureState(ctx, q, tenant, req.ConversationID, c.queueID)
	if err != nil {
		return Decision{}, err
	}
	save := func(d Decision, state string) (Decision, error) {
		return persist(ctx, q, tenant, req.ConversationID, episode, trigger, d, state, c.queueID)
	}
	switch {
	case c.status != "open":
		return save(Decision{Kind: NoAction, Reason: ReasonClosed}, "done")
	case c.assigned != nil: // somebody (a person, an earlier run) already owns it: never reassign
		return save(Decision{Kind: AssignAgent, Reason: ReasonAlreadyAssigned, Destination: "agent:" + c.assigned.String(), AssignedUser: *c.assigned}, "assigned")
	case c.queueID == nil:
		return save(Decision{Kind: NoAction, Reason: ReasonNoQueue}, "done")
	case c.mode == "manual" || c.mode == "":
		return save(Decision{Kind: Enqueue, Reason: ReasonManualQueue, Destination: "queue:" + c.queueID.String()}, "queued")
	}
	queue := *c.queueID
	var now time.Time
	if err := q.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return Decision{}, err
	}
	// 3. the sector's calendar
	avail, err := sectorsadapters.QueueAvailability(ctx, q, tenant, queue, now)
	if err != nil {
		return Decision{}, err
	}
	if !avail.Open {
		if avail.NextOpen == nil {
			return save(Decision{Kind: RequireManualAction, Reason: ReasonNeverOpens, Destination: "queue:" + queue.String()}, "manual_required")
		}
		if err := planWakeup(ctx, q, tenant, req.ConversationID, episode, *avail.NextOpen); err != nil {
			return Decision{}, err
		}
		return save(Decision{Kind: WaitUntilOpen, Reason: ReasonSectorClosed, Destination: "queue:" + queue.String(), WakeAt: avail.NextOpen}, "waiting_open")
	}
	// 4. open: who may receive it, by the single eligibility rule, in the order of the queue's strategy
	snap, err := sectorsadapters.Snapshot(ctx, q, tenant, queue, now, e.presence)
	if err != nil {
		return Decision{}, err
	}
	candidates := order(snap.Agents, c.mode)
	anyPresenceDown := false
	for _, a := range snap.Agents {
		for _, r := range a.Reasons {
			if r == eligibility.PresenceUnavailable {
				anyPresenceDown = true
			}
		}
	}
	for _, cand := range candidates {
		ok, err := tryAssign(ctx, q, tenant, queue, req.ConversationID, cand, req.StrategyReason)
		if err != nil {
			return Decision{}, err
		}
		if ok {
			if err := cancelWakeup(ctx, q, tenant, req.ConversationID, episode); err != nil {
				return Decision{}, err
			}
			return save(Decision{Kind: AssignAgent, Reason: ReasonAssigned, Destination: "agent:" + cand.UserID.String(), AssignedUser: cand.UserID}, "assigned")
		}
		// lost a race (capacity, lock, or the conversation was taken meanwhile): look at the conversation again before the next candidate
		var owner *uuid.UUID
		if err := q.QueryRow(ctx, `SELECT assigned_to_user_id FROM conversations WHERE tenant_id=$1 AND id=$2`, tenant, req.ConversationID).Scan(&owner); err != nil {
			return Decision{}, err
		}
		if owner != nil {
			return save(Decision{Kind: AssignAgent, Reason: ReasonAlreadyAssigned, Destination: "agent:" + owner.String(), AssignedUser: *owner}, "assigned")
		}
	}
	reason := ReasonNoEligibleAgent
	switch {
	case anyPresenceDown:
		reason = ReasonPresenceDown
	case onlyCapacity(snap.Agents):
		reason = ReasonCapacityExhausted
	}
	d, err := save(Decision{Kind: Enqueue, Reason: reason, Destination: "queue:" + queue.String()}, "queued")
	if err != nil {
		return Decision{}, err
	}
	if anyPresenceDown { // the legacy contract: a presence backend that cannot answer is retried, never folded into "nobody available"
		return d, ports.ErrPresenceUnavailable
	}
	return d, nil
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

// tryAssign is the atomic step for ONE candidate: a try-lock per agent (never wait: a busy agent is skipped, like SKIP LOCKED, so two
// workers cannot deadlock on each other's candidates), capacity re-counted under that lock, then the owner is set only if still empty.
func tryAssign(ctx context.Context, q platformdb.Querier, tenant, queue, conversation uuid.UUID, cand sectorsadapters.AgentVerdict, reason string) (bool, error) {
	if reason == "" {
		reason = "round_robin"
	}
	var locked bool
	if err := q.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('routing:agent:' || $1::text || ':' || $2::text, 0))`, tenant, cand.UserID).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	var workload int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM conversations WHERE tenant_id=$1 AND queue_id=$2 AND assigned_to_user_id=$3 AND status='open'`, tenant, queue, cand.UserID).Scan(&workload); err != nil {
		return false, err
	}
	if workload >= cand.Capacity {
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

func ensureState(ctx context.Context, q platformdb.Querier, tenant, conversation uuid.UUID, queue *uuid.UUID) (int, error) {
	var episode int
	err := q.QueryRow(ctx, `INSERT INTO routing_engine_state (tenant_id, conversation_id, state, queue_id) VALUES ($1,$2,'queued',$3)
		ON CONFLICT (tenant_id, conversation_id) DO UPDATE SET queue_id=COALESCE(EXCLUDED.queue_id, routing_engine_state.queue_id)
		RETURNING episode`, tenant, conversation, queue).Scan(&episode)
	if err != nil {
		return 0, fmt.Errorf("engine: routing state: %w", err)
	}
	// lock it for the rest of the decision (the conversation row already serializes, this keeps readers of the state consistent)
	if _, err := q.Exec(ctx, `SELECT 1 FROM routing_engine_state WHERE tenant_id=$1 AND conversation_id=$2 FOR UPDATE`, tenant, conversation); err != nil {
		return 0, err
	}
	return episode, nil
}

// persist writes the decision (one row per episode, step and trigger: a repeat updates it and counts the attempt) and the new state.
func persist(ctx context.Context, q platformdb.Querier, tenant, conversation uuid.UUID, episode int, trigger string, d Decision, state string, queue *uuid.UUID) (Decision, error) {
	var attempts int
	if err := q.QueryRow(ctx, `INSERT INTO routing_engine_decisions (tenant_id, conversation_id, episode, step_index, trigger, decision, destination, reason)
		VALUES ($1,$2,$3,0,$4,$5,$6,$7)
		ON CONFLICT (tenant_id, conversation_id, episode, step_index, trigger)
		DO UPDATE SET decision=EXCLUDED.decision, destination=EXCLUDED.destination, reason=EXCLUDED.reason, attempts=routing_engine_decisions.attempts+1, updated_at=now()
		RETURNING attempts`, tenant, conversation, episode, trigger, string(d.Kind), d.Destination, d.Reason).Scan(&attempts); err != nil {
		return Decision{}, fmt.Errorf("engine: decision: %w", err)
	}
	if _, err := q.Exec(ctx, `UPDATE routing_engine_state SET state=$3, last_decision=$4, queue_id=COALESCE($5, queue_id), updated_at=now() WHERE tenant_id=$1 AND conversation_id=$2`,
		tenant, conversation, state, string(d.Kind), queue); err != nil {
		return Decision{}, err
	}
	d.Attempts = attempts
	return d, nil
}

func planWakeup(ctx context.Context, q platformdb.Querier, tenant, conversation uuid.UUID, episode int, due time.Time) error {
	// one timer per (conversation, episode, kind): planning again moves it. A moved due time bumps the generation, so a worker that
	// claimed the old plan cannot complete the new one.
	_, err := q.Exec(ctx, `INSERT INTO routing_engine_timers (tenant_id, conversation_id, episode, kind, step_index, due_at) VALUES ($1,$2,$3,'open_wakeup',0,$4)
		ON CONFLICT (tenant_id, conversation_id, episode, kind, step_index)
		DO UPDATE SET due_at=EXCLUDED.due_at, state='pending', lease_until=NULL,
		  generation=CASE WHEN routing_engine_timers.due_at IS DISTINCT FROM EXCLUDED.due_at THEN routing_engine_timers.generation+1 ELSE routing_engine_timers.generation END,
		  updated_at=now()`, tenant, conversation, episode, due)
	return err
}

func cancelWakeup(ctx context.Context, q platformdb.Querier, tenant, conversation uuid.UUID, episode int) error {
	_, err := q.Exec(ctx, `UPDATE routing_engine_timers SET state='cancelled', updated_at=now() WHERE tenant_id=$1 AND conversation_id=$2 AND episode=$3 AND kind='open_wakeup' AND state IN ('pending','leased')`, tenant, conversation, episode)
	return err
}
