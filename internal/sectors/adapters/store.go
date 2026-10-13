// Package adapters persists and serves the sectors and operational calendars (ADR-0044 phase 3). It runs inside the request's tenant
// session (RLS decides the rows) and never opens its own transaction; the handlers check agent.read / agent.manage.
package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/scheduling"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInUse    = errors.New("in use")
	ErrStale    = errors.New("calendar changed since it was read")
)

// Calendar is a stored calendar: its definition (spec), its emergency suspensions, and the revision that timers derive from.
type Calendar struct {
	ID          uuid.UUID       `json:"id"`
	Name        string          `json:"name"`
	Revision    int             `json:"revision"`
	Spec        scheduling.Spec `json:"definition"`
	Suspensions []Suspension    `json:"suspensions"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type Suspension struct {
	ID     uuid.UUID `json:"id"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Reason string    `json:"reason,omitempty"`
}

// Schedule assembles the domain schedule (definition + the suspensions still relevant).
func (c Calendar) Schedule() (scheduling.Schedule, error) {
	s, err := c.Spec.Schedule()
	if err != nil {
		return scheduling.Schedule{}, err
	}
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	for _, sp := range c.Suspensions {
		if sp.To.After(cutoff) {
			s.Suspensions = append(s.Suspensions, scheduling.Suspension{From: sp.From, To: sp.To, Reason: sp.Reason})
		}
	}
	return s, s.Validate()
}

func LoadCalendar(ctx context.Context, q platformdb.Querier, tenant, id uuid.UUID) (*Calendar, error) {
	var c Calendar
	var weekly []byte
	err := q.QueryRow(ctx, `SELECT id, name, revision, timezone, kind, weekly, updated_at FROM calendars WHERE tenant_id=$1 AND id=$2`, tenant, id).
		Scan(&c.ID, &c.Name, &c.Revision, &c.Spec.Timezone, &c.Spec.Kind, &weekly, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(weekly, &c.Spec.Weekly); err != nil {
		return nil, fmt.Errorf("calendar %s: stored weekly rules unreadable: %w", id, err)
	}
	rows, err := q.Query(ctx, `SELECT name, from_date::text, to_date::text, closed, intervals FROM calendar_exceptions WHERE tenant_id=$1 AND calendar_id=$2 ORDER BY from_date, id`, tenant, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e scheduling.SpecException
		var iv []byte
		if err := rows.Scan(&e.Name, &e.From, &e.To, &e.Closed, &iv); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(iv, &e.Intervals); err != nil {
			rows.Close()
			return nil, err
		}
		c.Spec.Exceptions = append(c.Spec.Exceptions, e)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	rows, err = q.Query(ctx, `SELECT name, on_date::text, intervals FROM calendar_holidays WHERE tenant_id=$1 AND calendar_id=$2 ORDER BY on_date`, tenant, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var h scheduling.SpecHoliday
		var iv []byte
		if err := rows.Scan(&h.Name, &h.Date, &iv); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(iv, &h.Intervals); err != nil {
			rows.Close()
			return nil, err
		}
		c.Spec.Holidays = append(c.Spec.Holidays, h)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	c.Suspensions = []Suspension{}
	rows, err = q.Query(ctx, `SELECT id, starts_at, ends_at, reason FROM calendar_suspensions WHERE tenant_id=$1 AND calendar_id=$2 AND ends_at > now() - interval '30 days' ORDER BY starts_at, id`, tenant, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s Suspension
		if err := rows.Scan(&s.ID, &s.From, &s.To, &s.Reason); err != nil {
			return nil, err
		}
		c.Suspensions = append(c.Suspensions, s)
	}
	return &c, rows.Err()
}

// SaveDefinition replaces the whole definition of a calendar in the caller's transaction: the calendar row is locked first, so two
// editors serialize, and the revision is bumped so everything derived from the old definition (timers) can tell it is stale.
func SaveDefinition(ctx context.Context, q platformdb.Querier, tenant, id uuid.UUID, name string, sp scheduling.Spec, expectedRevision *int) (int, error) {
	if _, err := sp.Schedule(); err != nil {
		return 0, err
	}
	var current int
	if err := q.QueryRow(ctx, `SELECT revision FROM calendars WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, id).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if expectedRevision != nil && *expectedRevision != current {
		return 0, ErrStale
	}
	weekly, _ := json.Marshal(nonNil(sp.Weekly))
	var rev int
	err := q.QueryRow(ctx, `UPDATE calendars SET name=$3, timezone=$4, kind=$5, weekly=$6, revision=revision+1, updated_at=now()
		WHERE tenant_id=$1 AND id=$2 RETURNING revision`, tenant, id, name, sp.Timezone, string(sp.Kind), weekly).Scan(&rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if err := writeChildren(ctx, q, tenant, id, sp); err != nil {
		return 0, err
	}
	return rev, nil
}

func CreateCalendar(ctx context.Context, q platformdb.Querier, tenant uuid.UUID, name string, sp scheduling.Spec) (uuid.UUID, error) {
	if _, err := sp.Schedule(); err != nil {
		return uuid.Nil, err
	}
	weekly, _ := json.Marshal(nonNil(sp.Weekly))
	id := uuid.New()
	if _, err := q.Exec(ctx, `INSERT INTO calendars (id, tenant_id, name, timezone, kind, weekly) VALUES ($1,$2,$3,$4,$5,$6)`,
		id, tenant, name, sp.Timezone, string(sp.Kind), weekly); err != nil {
		return uuid.Nil, err
	}
	return id, writeChildren(ctx, q, tenant, id, sp)
}

func writeChildren(ctx context.Context, q platformdb.Querier, tenant, id uuid.UUID, sp scheduling.Spec) error {
	if _, err := q.Exec(ctx, `DELETE FROM calendar_exceptions WHERE tenant_id=$1 AND calendar_id=$2`, tenant, id); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `DELETE FROM calendar_holidays WHERE tenant_id=$1 AND calendar_id=$2`, tenant, id); err != nil {
		return err
	}
	for _, e := range sp.Exceptions {
		iv, _ := json.Marshal(nonNilIv(e.Intervals))
		if _, err := q.Exec(ctx, `INSERT INTO calendar_exceptions (tenant_id, calendar_id, name, from_date, to_date, closed, intervals) VALUES ($1,$2,$3,$4::date,$5::date,$6,$7)`,
			tenant, id, e.Name, e.From, e.To, e.Closed, iv); err != nil {
			return err
		}
	}
	for _, h := range sp.Holidays {
		iv, _ := json.Marshal(nonNilIv(h.Intervals))
		if _, err := q.Exec(ctx, `INSERT INTO calendar_holidays (tenant_id, calendar_id, name, on_date, intervals) VALUES ($1,$2,$3,$4::date,$5)`,
			tenant, id, h.Name, h.Date, iv); err != nil {
			return err
		}
	}
	return nil
}

func nonNil(v []scheduling.SpecDay) []scheduling.SpecDay {
	if v == nil {
		return []scheduling.SpecDay{}
	}
	return v
}

func nonNilIv(v []scheduling.SpecInterval) []scheduling.SpecInterval {
	if v == nil {
		return []scheduling.SpecInterval{}
	}
	return v
}

// Effective is the calendar that applies to a queue and where it came from.
type Effective struct {
	CalendarID *uuid.UUID
	Source     string // queue | sector | tenant | none
}

// EffectiveCalendar resolves the inheritance queue -> sector -> tenant default -> none (always open, as before ADR-0044).
func EffectiveCalendar(ctx context.Context, q platformdb.Querier, tenant, queueID uuid.UUID) (Effective, error) {
	var qc, sc, tc *uuid.UUID
	err := q.QueryRow(ctx, `SELECT q.calendar_id, s.calendar_id, t.default_calendar_id
		FROM queues q LEFT JOIN sectors s ON s.tenant_id=q.tenant_id AND s.id=q.sector_id JOIN tenants t ON t.id=q.tenant_id
		WHERE q.tenant_id=$1 AND q.id=$2`, tenant, queueID).Scan(&qc, &sc, &tc)
	if errors.Is(err, pgx.ErrNoRows) {
		return Effective{}, ErrNotFound
	}
	if err != nil {
		return Effective{}, err
	}
	switch {
	case qc != nil:
		return Effective{qc, "queue"}, nil
	case sc != nil:
		return Effective{sc, "sector"}, nil
	case tc != nil:
		return Effective{tc, "tenant"}, nil
	}
	return Effective{nil, "none"}, nil
}

// Availability answers "is this queue open now, and when does that change?" for the routing engine, the Flow Builder node and the UI.
type Availability struct {
	Open           bool       `json:"open"`
	At             time.Time  `json:"at"`
	CalendarSource string     `json:"calendar_source"`
	CalendarID     *uuid.UUID `json:"calendar_id,omitempty"`
	Timezone       string     `json:"timezone,omitempty"`
	NextOpen       *time.Time `json:"next_open,omitempty"`
	NextClose      *time.Time `json:"next_close,omitempty"`
}

func QueueAvailability(ctx context.Context, q platformdb.Querier, tenant, queueID uuid.UUID, at time.Time) (Availability, error) {
	eff, err := EffectiveCalendar(ctx, q, tenant, queueID)
	if err != nil {
		return Availability{}, err
	}
	a := Availability{At: at, CalendarSource: eff.Source, CalendarID: eff.CalendarID}
	if eff.CalendarID == nil { // no calendar anywhere: open, exactly the behaviour before sectors and calendars existed
		a.Open = true
		return a, nil
	}
	cal, err := LoadCalendar(ctx, q, tenant, *eff.CalendarID)
	if err != nil {
		return Availability{}, err
	}
	s, err := cal.Schedule()
	if err != nil {
		return Availability{}, err
	}
	a.Timezone = s.Timezone
	if a.Open, err = s.IsOpen(at); err != nil {
		return Availability{}, err
	}
	if n, ok, err := s.NextOpen(at); err == nil && ok && !a.Open {
		a.NextOpen = &n
	}
	if c, ok, err := s.NextClose(at); err == nil && ok {
		a.NextClose = &c
	}
	return a, nil
}

// ReplanWaiting recomputes the open-wakeup timer of every conversation of the tenant that is waiting for its sector to open. It is
// called in the SAME transaction as any change that can move an opening (a calendar save, a suspension, a link between queue, sector
// and calendar, the tenant default), so nothing waits for an hour that no longer exists. A conversation whose queue is open now is
// woken immediately; one that never opens within the horizon is handed to a person.
//
// Lock order is the engine's: conversation -> routing state -> timer. Each conversation is locked and its state re-read before anything
// is changed, so a decision that committed meanwhile (an assignment) is never overwritten. Paged by conversation id, with a ceiling.
func ReplanWaiting(ctx context.Context, q platformdb.Querier, tenant uuid.UUID) (int, error) {
	n, err := replanOpenWakeups(ctx, q, tenant)
	if err != nil {
		return n, err
	}
	m, err := ReplanStepWaits(ctx, q, tenant)
	if err != nil {
		return n + m, err
	}
	k, err := ReplanSLA(ctx, q, tenant)
	return n + m + k, err
}

func replanOpenWakeups(ctx context.Context, q platformdb.Querier, tenant uuid.UUID) (int, error) {
	var now time.Time
	if err := q.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return 0, err
	}
	const pageSize, maxPages = 500, 200
	byQueue := map[uuid.UUID]Availability{}
	n := 0
	cursor := uuid.Nil
	for page := 0; page < maxPages; page++ {
		rows, err := q.Query(ctx, `SELECT conversation_id FROM routing_engine_state WHERE tenant_id=$1 AND state='waiting_open' AND conversation_id > $2 ORDER BY conversation_id LIMIT $3`, tenant, cursor, pageSize)
		if err != nil {
			return n, err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return n, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if rows.Err() != nil {
			return n, rows.Err()
		}
		if len(ids) == 0 {
			return n, nil
		}
		cursor = ids[len(ids)-1]
		for _, conv := range ids {
			var found int
			if err := q.QueryRow(ctx, `SELECT 1 FROM conversations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, conv).Scan(&found); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return n, err
			}
			var episode int
			var queue *uuid.UUID
			if err := q.QueryRow(ctx, `SELECT episode, queue_id FROM routing_engine_state WHERE tenant_id=$1 AND conversation_id=$2 AND state='waiting_open' FOR UPDATE`, tenant, conv).Scan(&episode, &queue); err != nil {
				if errors.Is(err, pgx.ErrNoRows) { // it moved on while we were looking (assigned, handed to a person): leave it alone
					continue
				}
				return n, err
			}
			if queue == nil {
				continue
			}
			a, ok := byQueue[*queue]
			if !ok {
				var err error
				if a, err = QueueAvailability(ctx, q, tenant, *queue, now); err != nil {
					return n, err
				}
				byQueue[*queue] = a
			}
			due := now
			if !a.Open {
				if a.NextOpen == nil { // never opens within the horizon: a person decides
					if _, err := q.Exec(ctx, `UPDATE routing_engine_state SET state='manual_required', last_decision='REQUIRE_MANUAL_ACTION', updated_at=now() WHERE tenant_id=$1 AND conversation_id=$2 AND episode=$3 AND state='waiting_open'`, tenant, conv, episode); err != nil {
						return n, err
					}
					if _, err := q.Exec(ctx, `UPDATE routing_engine_timers SET state='cancelled', claim_token=NULL, updated_at=now() WHERE tenant_id=$1 AND conversation_id=$2 AND episode=$3 AND kind='open_wakeup' AND state IN ('pending','leased')`, tenant, conv, episode); err != nil {
						return n, err
					}
					n++
					continue
				}
				due = *a.NextOpen
			}
			if _, err := q.Exec(ctx, `INSERT INTO routing_engine_timers (tenant_id, conversation_id, episode, kind, step_index, due_at) VALUES ($1,$2,$3,'open_wakeup',0,$4)
				ON CONFLICT (tenant_id, conversation_id, episode, kind, step_index)
				DO UPDATE SET due_at=EXCLUDED.due_at, state='pending', lease_until=NULL, claim_token=NULL, attempts=0, generation=routing_engine_timers.generation+1, updated_at=now()`, tenant, conv, episode, due); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// CalendarOf returns the schedule that applies to a queue (inheritance queue -> sector -> tenant default); ok=false when none applies
// (the queue is always open).
func CalendarOf(ctx context.Context, q platformdb.Querier, tenant, queueID uuid.UUID) (scheduling.Schedule, bool, error) {
	eff, err := EffectiveCalendar(ctx, q, tenant, queueID)
	if err != nil || eff.CalendarID == nil {
		return scheduling.Schedule{}, false, err
	}
	cal, err := LoadCalendar(ctx, q, tenant, *eff.CalendarID)
	if err != nil {
		return scheduling.Schedule{}, false, err
	}
	s, err := cal.Schedule()
	return s, err == nil, err
}

// Elapsed is the time between two instants as a wait is counted: wall-clock, or only the open time of the queue's calendar.
func Elapsed(ctx context.Context, q platformdb.Querier, tenant, queueID uuid.UUID, from, to time.Time, business bool) (time.Duration, error) {
	if !to.After(from) {
		return 0, nil
	}
	if !business {
		return to.Sub(from), nil
	}
	s, ok, err := CalendarOf(ctx, q, tenant, queueID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return to.Sub(from), nil
	}
	return s.BusinessElapsed(from, to)
}

// DueAfter is the instant at which a wait of d, counted from `from`, ends (wall-clock or in the queue's business time). A business wait
// that the calendar cannot satisfy within its horizon ends at the horizon: a timer must always exist.
func DueAfter(ctx context.Context, q platformdb.Querier, tenant, queueID uuid.UUID, from time.Time, d time.Duration, business bool) (time.Time, error) {
	if !business {
		return from.Add(d), nil
	}
	s, ok, err := CalendarOf(ctx, q, tenant, queueID)
	if err != nil {
		return time.Time{}, err
	}
	if !ok {
		return from.Add(d), nil
	}
	due, found, err := s.AddBusiness(from, d)
	if err != nil {
		return time.Time{}, err
	}
	if !found {
		return from.Add(scheduling.Horizon), nil
	}
	return due, nil
}

// ReplanStepWaits recomputes the due time of the pending step_wait timers whose wait is counted in BUSINESS time: a calendar change can
// make them fall earlier or later. Same lock order as the engine (conversation, then timer), paged.
func ReplanStepWaits(ctx context.Context, q platformdb.Querier, tenant uuid.UUID) (int, error) {
	const pageSize, maxPages = 500, 100
	n := 0
	cursor := uuid.Nil
	for page := 0; page < maxPages; page++ {
		rows, err := q.Query(ctx, `SELECT t.conversation_id, t.episode, t.step_index, st.origin_queue_id, st.step_entered_at, s.wait_seconds
			FROM routing_engine_timers t
			JOIN routing_engine_state st ON st.tenant_id=t.tenant_id AND st.conversation_id=t.conversation_id AND st.episode=t.episode
			JOIN overflow_policies p ON p.tenant_id=t.tenant_id AND p.queue_id=st.origin_queue_id AND p.active
			JOIN overflow_steps s ON s.tenant_id=t.tenant_id AND s.policy_id=p.id AND s.position=t.step_index AND s.wait_business
			WHERE t.tenant_id=$1 AND t.kind='step_wait' AND t.state='pending' AND t.conversation_id > $2
			ORDER BY t.conversation_id LIMIT $3`, tenant, cursor, pageSize)
		if err != nil {
			return n, err
		}
		type item struct {
			conv        uuid.UUID
			episode     int
			step        int
			origin      uuid.UUID
			entered     time.Time
			waitSeconds int
		}
		var items []item
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.conv, &it.episode, &it.step, &it.origin, &it.entered, &it.waitSeconds); err != nil {
				rows.Close()
				return n, err
			}
			items = append(items, it)
		}
		rows.Close()
		if rows.Err() != nil {
			return n, rows.Err()
		}
		if len(items) == 0 {
			return n, nil
		}
		cursor = items[len(items)-1].conv
		for _, it := range items {
			var found int
			if err := q.QueryRow(ctx, `SELECT 1 FROM conversations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, it.conv).Scan(&found); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return n, err
			}
			due, err := DueAfter(ctx, q, tenant, it.origin, it.entered, time.Duration(it.waitSeconds)*time.Second, true)
			if err != nil {
				return n, err
			}
			tag, err := q.Exec(ctx, `UPDATE routing_engine_timers SET due_at=$5, claim_token=NULL, generation=generation+1, updated_at=now()
				WHERE tenant_id=$1 AND conversation_id=$2 AND episode=$3 AND kind='step_wait' AND step_index=$4 AND state='pending' AND due_at IS DISTINCT FROM $5`, tenant, it.conv, it.episode, it.step, due)
			if err != nil {
				return n, err
			}
			n += int(tag.RowsAffected())
		}
	}
	return n, nil
}

// KickQueue asks the engine to look again, soon, at every open unassigned conversation that belongs to a queue's chain (the queue itself, or
// conversations that overflowed away from it): used when its overflow policy is saved, replaced, deactivated or removed, so a conversation
// waiting under the OLD policy (or under none, in a manual queue that has no routing job) is re-evaluated under the NEW one. A 'retry'
// timer due now runs the same decision as any other trigger.
func KickQueue(ctx context.Context, q platformdb.Querier, tenant, queueID uuid.UUID) (int, error) {
	const pageSize, maxPages = 500, 100
	n := 0
	cursor := uuid.Nil
	for page := 0; page < maxPages; page++ {
		rows, err := q.Query(ctx, `SELECT c.id FROM conversations c
			LEFT JOIN routing_engine_state st ON st.tenant_id=c.tenant_id AND st.conversation_id=c.id
			WHERE c.tenant_id=$1 AND c.status='open' AND c.assigned_to_user_id IS NULL AND c.id > $2
			  AND (c.queue_id=$3 OR st.origin_queue_id=$3)
			ORDER BY c.id LIMIT $4`, tenant, cursor, queueID, pageSize)
		if err != nil {
			return n, err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return n, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if rows.Err() != nil {
			return n, rows.Err()
		}
		if len(ids) == 0 {
			return n, nil
		}
		cursor = ids[len(ids)-1]
		for _, id := range ids {
			var found int
			if err := q.QueryRow(ctx, `SELECT 1 FROM conversations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, id).Scan(&found); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return n, err
			}
			var episode int
			err := q.QueryRow(ctx, `SELECT COALESCE((SELECT episode FROM routing_engine_state WHERE tenant_id=$1 AND conversation_id=$2), 1)`, tenant, id).Scan(&episode)
			if err != nil {
				return n, err
			}
			// the episode of a conversation the engine has not seen yet is 1: Decide creates its state with that episode
			if _, err := q.Exec(ctx, `INSERT INTO routing_engine_timers (tenant_id, conversation_id, episode, kind, step_index, due_at) VALUES ($1,$2,$3,'retry',0,now())
				ON CONFLICT (tenant_id, conversation_id, episode, kind, step_index)
				DO UPDATE SET due_at=now(), state='pending', lease_until=NULL, claim_token=NULL, attempts=0, generation=routing_engine_timers.generation+1, updated_at=now()`, tenant, id, episode); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// ReplanSLA recomputes the risk point and deadline of the running BUSINESS clocks of the tenant after a calendar change (a holiday added, a
// suspension, a new schedule): the remaining budget, counted from the clock's anchor on the calendar of the place the conversation is in,
// gives new instants, and the durable sla timers follow. Wall-clock clocks do not depend on a calendar and are left alone. Same lock order
// as the engine (conversation, then clock, then timer), paged.
func ReplanSLA(ctx context.Context, q platformdb.Querier, tenant uuid.UUID) (int, error) {
	const pageSize, maxPages = 500, 100
	n := 0
	cursor := uuid.Nil
	for page := 0; page < maxPages; page++ {
		rows, err := q.Query(ctx, `SELECT s.conversation_id FROM conversation_sla s
			WHERE s.tenant_id=$1 AND s.state='running' AND s.counting='business' AND s.calendar_queue_id IS NOT NULL AND s.conversation_id > $2
			GROUP BY s.conversation_id ORDER BY s.conversation_id LIMIT $3`, tenant, cursor, pageSize)
		if err != nil {
			return n, err
		}
		var convs []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return n, err
			}
			convs = append(convs, id)
		}
		rows.Close()
		if rows.Err() != nil {
			return n, rows.Err()
		}
		if len(convs) == 0 {
			return n, nil
		}
		cursor = convs[len(convs)-1]
		for _, conv := range convs {
			var found int
			if err := q.QueryRow(ctx, `SELECT 1 FROM conversations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, conv).Scan(&found); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return n, err
			}
			var episode int
			if err := q.QueryRow(ctx, `SELECT COALESCE((SELECT episode FROM routing_engine_state WHERE tenant_id=$1 AND conversation_id=$2), 1)`, tenant, conv).Scan(&episode); err != nil {
				return n, err
			}
			crows, err := q.Query(ctx, `SELECT id, metric, calendar_queue_id, anchor_at, consumed_seconds, budget_seconds, risk_percent, risk_at, deadline_at, risk_notified_at
				FROM conversation_sla WHERE tenant_id=$1 AND conversation_id=$2 AND state='running' AND counting='business' AND calendar_queue_id IS NOT NULL ORDER BY id FOR UPDATE`, tenant, conv)
			if err != nil {
				return n, err
			}
			type clk struct {
				id                        uuid.UUID
				metric                    string
				cal                       uuid.UUID
				anchor                    time.Time
				consumed, budget, riskPct int
				riskAt, deadline          time.Time
				notified                  *time.Time
			}
			var clocks []clk
			for crows.Next() {
				var c clk
				if err := crows.Scan(&c.id, &c.metric, &c.cal, &c.anchor, &c.consumed, &c.budget, &c.riskPct, &c.riskAt, &c.deadline, &c.notified); err != nil {
					crows.Close()
					return n, err
				}
				clocks = append(clocks, c)
			}
			crows.Close()
			if crows.Err() != nil {
				return n, crows.Err()
			}
			for _, c := range clocks {
				total := time.Duration(c.budget) * time.Second
				consumed := time.Duration(c.consumed) * time.Second
				left := func(d time.Duration) time.Duration {
					if d -= consumed; d < 0 {
						return 0
					}
					return d
				}
				deadline, err := DueAfter(ctx, q, tenant, c.cal, c.anchor, left(total), true)
				if err != nil {
					return n, err
				}
				risk, err := DueAfter(ctx, q, tenant, c.cal, c.anchor, left(total*time.Duration(c.riskPct)/100), true)
				if err != nil {
					return n, err
				}
				if deadline.Equal(c.deadline) && risk.Equal(c.riskAt) {
					continue
				}
				if _, err := q.Exec(ctx, `UPDATE conversation_sla SET deadline_at=$2, risk_at=$3, updated_at=now() WHERE id=$1`, c.id, deadline, risk); err != nil {
					return n, err
				}
				step := map[string]int{"first_response": 1, "queue_wait": 2, "assignment": 3, "resolution": 4}[c.metric]
				for kind, due := range map[string]time.Time{"sla_risk": risk, "sla_breach": deadline} {
					if kind == "sla_risk" && c.notified != nil {
						continue // already reported: no second risk timer
					}
					if _, err := q.Exec(ctx, `INSERT INTO routing_engine_timers (tenant_id, conversation_id, episode, kind, step_index, due_at) VALUES ($1,$2,$3,$4,$5,$6)
						ON CONFLICT (tenant_id, conversation_id, episode, kind, step_index)
						DO UPDATE SET due_at=EXCLUDED.due_at, state='pending', lease_until=NULL, claim_token=NULL, attempts=0, generation=routing_engine_timers.generation+1, updated_at=now()`,
						tenant, conv, episode, kind, step, due); err != nil {
						return n, err
					}
				}
				n++
			}
		}
	}
	return n, nil
}
