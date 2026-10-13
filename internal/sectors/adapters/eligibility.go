package adapters

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/eligibility"
	"github.com/omnira/omnira/internal/scheduling"
)

// PresenceChecker is the batch lookup the routing already uses (internal/presence/adapters.Store implements it).
type PresenceChecker interface {
	OnlineMembers(ctx context.Context, tenantID uuid.UUID, agentProfileIDs []uuid.UUID) (map[uuid.UUID]bool, error)
}

// AgentVerdict is one line of the queue snapshot: who could receive an automatic assignment now, and if not, every reason why.
type AgentVerdict struct {
	UserID         uuid.UUID            `json:"user_id"`
	AgentProfileID uuid.UUID            `json:"agent_profile_id"`
	Name           string               `json:"name"`
	Eligible       bool                 `json:"eligible"`
	Reasons        []eligibility.Reason `json:"reasons"`
	OnCall         bool                 `json:"on_call"`
	Workload       int                  `json:"workload"`
	Capacity       int                  `json:"capacity"`
	ShiftSource    string               `json:"shift_source"` // queue | sector | all | none
	// LastAssignedAt orders the round-robin; it is an input of the engine, not part of the monitoring answer.
	LastAssignedAt *time.Time `json:"-"`
}

type QueueSnapshot struct {
	QueueID     uuid.UUID `json:"queue_id"`
	At          time.Time `json:"at"`
	Open        bool      `json:"open"`
	PresenceReq bool      `json:"presence_required"`
	EligibleCnt int       `json:"eligible_count"`
	Truncated   bool      `json:"truncated,omitempty"`
	// CalendarOnly: the instant asked is not "now", so presence and workload (which only exist in real time) are NOT evaluated; the
	// verdict answers "by calendar, shift and membership alone". Never read an eligible agent here as "would be assigned".
	CalendarOnly bool `json:"calendar_only"`
	// WiredToRouter says whether the live router (AssignRoundRobin) applies this same rule today. It is false until ADR-0044 phase 4:
	// this snapshot is the rule the engine WILL apply, shown so supervisors and tests can see it first.
	WiredToRouter bool           `json:"wired_to_router"`
	Agents        []AgentVerdict `json:"agents"`
}

// nowTolerance: an instant this close to the database clock is "now" (presence and workload are then evaluated).
const nowTolerance = 90 * time.Second

const snapshotMaxAgents = 500
const presenceChunk = 50

type agentRow struct {
	userID, profileID uuid.UUID
	name              string
	member, onCall    bool
	active, available bool
	capacity          int
	workload          int
	lastAssigned      *time.Time
	shiftOpen         *bool
	shiftSource       string
}

// Snapshot evaluates every candidate of a queue at an instant. It is READ-ONLY (it assigns nothing): the routing engine and the
// monitoring screen both ask this same question, so the answer a supervisor sees is the rule the engine applies.
func Snapshot(ctx context.Context, q platformdb.Querier, tenant, queueID uuid.UUID, at time.Time, presence PresenceChecker) (QueueSnapshot, error) {
	return SnapshotWith(ctx, q, tenant, queueID, at, presence, SnapshotOptions{})
}

// SnapshotOptions narrows or widens one evaluation. The zero value is the monitoring snapshot.
type SnapshotOptions struct {
	Only         *uuid.UUID         // evaluate this one person (the engine re-checks the chosen candidate under its lock)
	SkipPresence bool               // do not read presence (already checked for the whole queue; Valkey is not transactional)
	Limit        int                // max people evaluated (default snapshotMaxAgents)
	Policy       eligibility.Policy // explicit, authorized relaxations of one chain step (closed destination, off-shift agent)
}

func SnapshotWith(ctx context.Context, q platformdb.Querier, tenant, queueID uuid.UUID, at time.Time, presence PresenceChecker, opt SnapshotOptions) (QueueSnapshot, error) {
	limit := opt.Limit
	if limit <= 0 {
		limit = snapshotMaxAgents
	}
	avail, err := QueueAvailability(ctx, q, tenant, queueID, at)
	if err != nil {
		return QueueSnapshot{}, err
	}
	var dbNow time.Time
	if err := q.QueryRow(ctx, `SELECT now()`).Scan(&dbNow); err != nil {
		return QueueSnapshot{}, err
	}
	realtime := at.Sub(dbNow) < nowTolerance && dbNow.Sub(at) < nowTolerance
	var sectorID *uuid.UUID
	var presenceReq bool
	if err := q.QueryRow(ctx, `SELECT q.sector_id, t.routing_require_presence FROM queues q JOIN tenants t ON t.id=q.tenant_id WHERE q.tenant_id=$1 AND q.id=$2`,
		tenant, queueID).Scan(&sectorID, &presenceReq); err != nil {
		return QueueSnapshot{}, ErrNotFound
	}
	rows := map[uuid.UUID]*agentRow{}
	// members of the queue
	mrows, err := q.Query(ctx, `SELECT qm.user_id, COALESCE(ap.id, '00000000-0000-0000-0000-000000000000'::uuid), COALESCE(u.display_name, ''),
		(m.status='active' AND ap.status='active'), qm.active AND qm.available, qm.capacity,
		(SELECT count(*) FROM conversations c WHERE c.tenant_id=qm.tenant_id AND c.queue_id=qm.queue_id AND c.assigned_to_user_id=qm.user_id AND c.status='open'),
		qm.last_assigned_at
		FROM queue_members qm
		LEFT JOIN users u ON u.id=qm.user_id
		LEFT JOIN memberships m ON m.tenant_id=qm.tenant_id AND m.user_id=qm.user_id
		LEFT JOIN agent_profiles ap ON ap.tenant_id=m.tenant_id AND ap.membership_id=m.id
		WHERE qm.tenant_id=$1 AND qm.queue_id=$2 AND ($4::uuid IS NULL OR qm.user_id=$4) ORDER BY qm.user_id LIMIT $3`, tenant, queueID, limit+1, opt.Only)
	if err != nil {
		return QueueSnapshot{}, err
	}
	for mrows.Next() {
		var r agentRow
		var active *bool
		if err := mrows.Scan(&r.userID, &r.profileID, &r.name, &active, &r.available, &r.capacity, &r.workload, &r.lastAssigned); err != nil {
			mrows.Close()
			return QueueSnapshot{}, err
		}
		r.member = true
		r.active = active != nil && *active
		rows[r.userID] = &r
	}
	mrows.Close()
	if mrows.Err() != nil {
		return QueueSnapshot{}, mrows.Err()
	}
	// people on call right now for this queue's sector (or tenant-wide rosters)
	orows, err := q.Query(ctx, `SELECT p.user_id, COALESCE(ap.id, '00000000-0000-0000-0000-000000000000'::uuid), COALESCE(u.display_name, ''),
		(m.status='active' AND ap.status='active'), max(p.capacity),
		(SELECT count(*) FROM conversations c WHERE c.tenant_id=p.tenant_id AND c.queue_id=$2 AND c.assigned_to_user_id=p.user_id AND c.status='open')
		FROM on_call_periods p JOIN on_call_rosters r ON r.tenant_id=p.tenant_id AND r.id=p.roster_id AND r.active
		LEFT JOIN users u ON u.id=p.user_id
		LEFT JOIN memberships m ON m.tenant_id=p.tenant_id AND m.user_id=p.user_id
		LEFT JOIN agent_profiles ap ON ap.tenant_id=m.tenant_id AND ap.membership_id=m.id
		WHERE p.tenant_id=$1 AND p.starts_at <= $3 AND p.ends_at > $3 AND (r.sector_id IS NULL OR r.sector_id IS NOT DISTINCT FROM $4) AND ($6::uuid IS NULL OR p.user_id=$6)
		GROUP BY p.user_id, p.tenant_id, ap.id, u.display_name, m.status, ap.status LIMIT $5`, tenant, queueID, at, sectorID, limit+1, opt.Only)
	if err != nil {
		return QueueSnapshot{}, err
	}
	for orows.Next() {
		var uid, prof uuid.UUID
		var name string
		var active *bool
		var capacity, workload int
		if err := orows.Scan(&uid, &prof, &name, &active, &capacity, &workload); err != nil {
			orows.Close()
			return QueueSnapshot{}, err
		}
		r := rows[uid]
		if r == nil {
			r = &agentRow{userID: uid, profileID: prof, name: name, active: active != nil && *active, workload: workload}
			rows[uid] = r
		}
		r.onCall = true
		if capacity > r.capacity {
			r.capacity = capacity
		}
	}
	orows.Close()
	if orows.Err() != nil {
		return QueueSnapshot{}, orows.Err()
	}
	truncated := len(rows) > limit
	ids := make([]uuid.UUID, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	if err := loadShifts(ctx, q, tenant, queueID, sectorID, at, ids, rows); err != nil {
		return QueueSnapshot{}, err
	}
	// presence, one batch per chunk (never one call per agent)
	online := map[uuid.UUID]bool{}
	presenceDown := false
	if presenceReq && realtime && !opt.SkipPresence {
		if presence == nil {
			presenceDown = true
		} else {
			for i := 0; i < len(ids) && !presenceDown; i += presenceChunk {
				end := i + presenceChunk
				if end > len(ids) {
					end = len(ids)
				}
				var profs []uuid.UUID
				for _, id := range ids[i:end] {
					if rows[id].profileID != uuid.Nil {
						profs = append(profs, rows[id].profileID)
					}
				}
				got, err := presence.OnlineMembers(ctx, tenant, profs)
				if err != nil {
					presenceDown = true // fail closed: never assume online
					break
				}
				for k, v := range got {
					online[k] = v
				}
			}
		}
	}
	snap := QueueSnapshot{QueueID: queueID, At: at, Open: avail.Open, PresenceReq: presenceReq && realtime && !opt.SkipPresence, Truncated: truncated, CalendarOnly: !realtime, Agents: []AgentVerdict{}}
	for _, id := range ids {
		r := rows[id]
		v := eligibility.Evaluate(eligibility.Facts{
			Member: r.member, OnCall: r.onCall, Active: r.active, Available: r.available, ShiftOpen: r.shiftOpen, QueueOpen: avail.Open,
			PresenceReq: presenceReq && realtime && !opt.SkipPresence, PresenceOK: online[r.profileID], PresenceDown: presenceDown, Workload: workload(r, realtime), Capacity: r.capacity,
		}, opt.Policy)
		if v.Eligible {
			snap.EligibleCnt++
		}
		reasons := v.Reasons
		if reasons == nil {
			reasons = []eligibility.Reason{}
		}
		src := r.shiftSource
		if src == "" {
			src = "none"
		}
		snap.Agents = append(snap.Agents, AgentVerdict{UserID: r.userID, AgentProfileID: r.profileID, Name: r.name, Eligible: v.Eligible, Reasons: reasons,
			OnCall: v.OnCall, Workload: r.workload, Capacity: r.capacity, ShiftSource: src, LastAssignedAt: r.lastAssigned})
	}
	return snap, nil
}

// loadShifts resolves, for each person, the most specific shift calendar (queue > sector > all) and whether it is open at the instant.
func loadShifts(ctx context.Context, q platformdb.Querier, tenant, queueID uuid.UUID, sectorID *uuid.UUID, at time.Time, users []uuid.UUID, rows map[uuid.UUID]*agentRow) error {
	if len(users) == 0 {
		return nil
	}
	srows, err := q.Query(ctx, `SELECT user_id, calendar_id,
		CASE WHEN queue_id IS NOT NULL THEN 3 WHEN sector_id IS NOT NULL THEN 2 ELSE 1 END
		FROM agent_shifts WHERE tenant_id=$1 AND user_id = ANY($2)
		  AND ((queue_id = $3) OR (sector_id IS NOT NULL AND sector_id = $4) OR (queue_id IS NULL AND sector_id IS NULL))`, tenant, users, queueID, sectorID)
	if err != nil {
		return err
	}
	type pick struct {
		cal  uuid.UUID
		rank int
	}
	best := map[uuid.UUID]pick{}
	for srows.Next() {
		var u, c uuid.UUID
		var rank int
		if err := srows.Scan(&u, &c, &rank); err != nil {
			srows.Close()
			return err
		}
		if b, ok := best[u]; !ok || rank > b.rank {
			best[u] = pick{c, rank}
		}
	}
	srows.Close()
	if srows.Err() != nil {
		return srows.Err()
	}
	open := map[uuid.UUID]bool{}
	for u, b := range best {
		o, ok := open[b.cal]
		if !ok {
			cal, err := LoadCalendar(ctx, q, tenant, b.cal)
			if err != nil {
				return err
			}
			s, err := cal.Schedule()
			if err != nil {
				return err
			}
			if o, err = isOpen(s, at); err != nil {
				return err
			}
			open[b.cal] = o
		}
		v := o
		rows[u].shiftOpen = &v
		rows[u].shiftSource = map[int]string{3: "queue", 2: "sector", 1: "all"}[b.rank]
	}
	return nil
}

func isOpen(s scheduling.Schedule, at time.Time) (bool, error) { return s.IsOpen(at) }

// workload counts only in real time; for another instant capacity is not evaluated (see QueueSnapshot.CalendarOnly).
func workload(r *agentRow, realtime bool) int {
	if !realtime {
		return 0
	}
	return r.workload
}

// AgentShift answers whether the person's own shift calendar is open at the instant for a queue (queue > sector > all scope); open is nil
// when the person has no shift (no restriction).
func AgentShift(ctx context.Context, q platformdb.Querier, tenant, user, queueID uuid.UUID, at time.Time) (open *bool, source string, err error) {
	var sectorID *uuid.UUID
	if err := q.QueryRow(ctx, `SELECT sector_id FROM queues WHERE tenant_id=$1 AND id=$2`, tenant, queueID).Scan(&sectorID); err != nil {
		return nil, "", err
	}
	rows := map[uuid.UUID]*agentRow{user: {userID: user}}
	if err := loadShifts(ctx, q, tenant, queueID, sectorID, at, []uuid.UUID{user}, rows); err != nil {
		return nil, "", err
	}
	r := rows[user]
	return r.shiftOpen, r.shiftSource, nil
}
