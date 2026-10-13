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
