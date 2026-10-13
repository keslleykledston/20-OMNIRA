package adapters

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// ---------------------------------------------------------------- agent shifts (the person's own calendar)

type shiftItem struct {
	UserID     uuid.UUID  `json:"user_id"`
	CalendarID uuid.UUID  `json:"calendar_id"`
	QueueID    *uuid.UUID `json:"queue_id,omitempty"`
	SectorID   *uuid.UUID `json:"sector_id,omitempty"`
}

// ListShifts: GET /shifts[?user_id=].
func (h *Handler) ListShifts(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.read")
	if !ok {
		return
	}
	var user *uuid.UUID
	if raw := r.URL.Query().Get("user_id"); raw != "" {
		u, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "invalid user_id", http.StatusBadRequest)
			return
		}
		user = &u
	}
	rows, err := q.Query(r.Context(), `SELECT user_id, calendar_id, queue_id, sector_id FROM agent_shifts WHERE tenant_id=$1 AND ($2::uuid IS NULL OR user_id=$2) ORDER BY user_id, created_at LIMIT 1000`, tc.TenantID, user)
	if err != nil {
		http.Error(w, "failed to list shifts", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := []shiftItem{}
	for rows.Next() {
		var it shiftItem
		if err := rows.Scan(&it.UserID, &it.CalendarID, &it.QueueID, &it.SectorID); err != nil {
			http.Error(w, "failed to read shifts", http.StatusInternalServerError)
			return
		}
		items = append(items, it)
	}
	write(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// SetShift: PUT /shifts {"user_id", "calendar_id": uuid|null, "queue_id"?, "sector_id"?}. null clears the shift of that scope.
func (h *Handler) SetShift(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	var in struct {
		UserID     uuid.UUID  `json:"user_id"`
		CalendarID *uuid.UUID `json:"calendar_id"`
		QueueID    *uuid.UUID `json:"queue_id"`
		SectorID   *uuid.UUID `json:"sector_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.UserID == uuid.Nil || (in.QueueID != nil && in.SectorID != nil) {
		http.Error(w, "user_id is required and the scope is a queue_id or a sector_id, not both", http.StatusBadRequest)
		return
	}
	if !h.isAttendant(r, q, tc.TenantID, in.UserID) {
		http.Error(w, "the person is not an active attendant of this company", http.StatusNotFound)
		return
	}
	if in.CalendarID == nil { // clear
		tag, err := q.Exec(r.Context(), `DELETE FROM agent_shifts WHERE tenant_id=$1 AND user_id=$2 AND queue_id IS NOT DISTINCT FROM $3 AND sector_id IS NOT DISTINCT FROM $4`, tc.TenantID, in.UserID, in.QueueID, in.SectorID)
		if err != nil {
			http.Error(w, "failed to clear the shift", http.StatusInternalServerError)
			return
		}
		if tag.RowsAffected() > 0 {
			h.record(r, auditdomain.ResourceAgentShift, in.UserID, auditdomain.ActionShiftCleared, map[string]any{"queue_id": idText(in.QueueID), "sector_id": idText(in.SectorID)})
		}
		write(w, http.StatusOK, map[string]any{"user_id": in.UserID, "calendar_id": nil})
		return
	}
	if !h.exists(r, q, `SELECT EXISTS(SELECT 1 FROM calendars WHERE tenant_id=$1 AND id=$2)`, tc.TenantID, *in.CalendarID) {
		http.Error(w, "calendar not found", http.StatusNotFound)
		return
	}
	if in.QueueID != nil && !h.exists(r, q, `SELECT EXISTS(SELECT 1 FROM queues WHERE tenant_id=$1 AND id=$2)`, tc.TenantID, *in.QueueID) {
		http.Error(w, "queue not found", http.StatusNotFound)
		return
	}
	if in.SectorID != nil && !h.exists(r, q, `SELECT EXISTS(SELECT 1 FROM sectors WHERE tenant_id=$1 AND id=$2)`, tc.TenantID, *in.SectorID) {
		http.Error(w, "sector not found", http.StatusNotFound)
		return
	}
	if _, err := q.Exec(r.Context(), `INSERT INTO agent_shifts (tenant_id, user_id, calendar_id, queue_id, sector_id) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (tenant_id, user_id, queue_id, sector_id) DO UPDATE SET calendar_id=EXCLUDED.calendar_id`, tc.TenantID, in.UserID, *in.CalendarID, in.QueueID, in.SectorID); err != nil {
		http.Error(w, "failed to set the shift", http.StatusInternalServerError)
		return
	}
	h.record(r, auditdomain.ResourceAgentShift, in.UserID, auditdomain.ActionShiftSet, map[string]any{"calendar_id": in.CalendarID.String(), "queue_id": idText(in.QueueID), "sector_id": idText(in.SectorID)})
	write(w, http.StatusOK, shiftItem{UserID: in.UserID, CalendarID: *in.CalendarID, QueueID: in.QueueID, SectorID: in.SectorID})
}

// isAttendant: an active membership of THIS tenant with an active agent profile. Nobody else can be scheduled or put on call; a Hub
// operator is never turned into a member to make this pass (the Hub serves through its own grants).
func (h *Handler) isAttendant(r *http.Request, q platformdb.Querier, tenant, user uuid.UUID) bool {
	return h.exists(r, q, `SELECT EXISTS(SELECT 1 FROM memberships m JOIN agent_profiles ap ON ap.tenant_id=m.tenant_id AND ap.membership_id=m.id
		WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND ap.status='active')`, tenant, user)
}

// ---------------------------------------------------------------- on-call rosters

type rosterItem struct {
	ID       uuid.UUID  `json:"id"`
	Name     string     `json:"name"`
	Kind     string     `json:"kind"`
	SectorID *uuid.UUID `json:"sector_id,omitempty"`
	Active   bool       `json:"active"`
	OnNow    int        `json:"on_now"`
}

func (h *Handler) ListRosters(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.read")
	if !ok {
		return
	}
	rows, err := q.Query(r.Context(), `SELECT r.id, r.name, r.kind, r.sector_id, r.active,
		(SELECT count(DISTINCT p.user_id) FROM on_call_periods p WHERE p.tenant_id=r.tenant_id AND p.roster_id=r.id AND p.starts_at <= now() AND p.ends_at > now())
		FROM on_call_rosters r WHERE r.tenant_id=$1 ORDER BY r.active DESC, r.name LIMIT 500`, tc.TenantID)
	if err != nil {
		http.Error(w, "failed to list rosters", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := []rosterItem{}
	for rows.Next() {
		var it rosterItem
		if err := rows.Scan(&it.ID, &it.Name, &it.Kind, &it.SectorID, &it.Active, &it.OnNow); err != nil {
			http.Error(w, "failed to read rosters", http.StatusInternalServerError)
			return
		}
		items = append(items, it)
	}
	write(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (h *Handler) CreateRoster(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	var in struct {
		Name     string     `json:"name"`
		Kind     string     `json:"kind"`
		SectorID *uuid.UUID `json:"sector_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	name, valid := cleanName(in.Name)
	if !valid || (in.Kind != "on_call" && in.Kind != "contingency") {
		http.Error(w, "name (1 to 60 characters) and kind (on_call or contingency) are required", http.StatusBadRequest)
		return
	}
	if in.SectorID != nil && !h.exists(r, q, `SELECT EXISTS(SELECT 1 FROM sectors WHERE tenant_id=$1 AND id=$2)`, tc.TenantID, *in.SectorID) {
		http.Error(w, "sector not found", http.StatusNotFound)
		return
	}
	// plain SELECT first: the common duplicate ends in a clean 409 instead of a unique violation that aborts the session transaction
	if h.exists(r, q, `SELECT EXISTS(SELECT 1 FROM on_call_rosters WHERE tenant_id=$1 AND name=$2)`, tc.TenantID, name) {
		http.Error(w, "a roster with this name already exists", http.StatusConflict)
		return
	}
	id := uuid.New()
	if err := attempt(r, q, func() error {
		_, err := q.Exec(r.Context(), `INSERT INTO on_call_rosters (id, tenant_id, name, kind, sector_id) VALUES ($1,$2,$3,$4,$5)`, id, tc.TenantID, name, in.Kind, in.SectorID)
		return err
	}); err != nil {
		if conflict(w, err, "a roster with this name already exists") {
			return
		}
		http.Error(w, "failed to create the roster", http.StatusInternalServerError)
		return
	}
	h.record(r, auditdomain.ResourceOnCallRoster, id, auditdomain.ActionRosterCreated, map[string]any{"name": name, "kind": in.Kind})
	write(w, http.StatusCreated, rosterItem{ID: id, Name: name, Kind: in.Kind, SectorID: in.SectorID, Active: true})
}

// UpdateRoster: PATCH /on-call/rosters/{roster_id} {name?, active?}. Deactivating takes everyone off call at once without deleting history.
func (h *Handler) UpdateRoster(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "roster_id")
	if !ok {
		return
	}
	var in struct {
		Name   *string `json:"name"`
		Active *bool   `json:"active"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Name == nil && in.Active == nil {
		http.Error(w, "name or active required", http.StatusBadRequest)
		return
	}
	var name string
	var active bool
	err := q.QueryRow(r.Context(), `SELECT name, active FROM on_call_rosters WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tc.TenantID, id).Scan(&name, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "roster not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read the roster", http.StatusInternalServerError)
		return
	}
	if in.Name != nil {
		n, valid := cleanName(*in.Name)
		if !valid {
			http.Error(w, "name must have 1 to 60 characters", http.StatusBadRequest)
			return
		}
		name = n
	}
	if in.Active != nil {
		active = *in.Active
	}
	if h.exists(r, q, `SELECT EXISTS(SELECT 1 FROM on_call_rosters WHERE tenant_id=$1 AND name=$2 AND id<>$3)`, tc.TenantID, name, id) {
		http.Error(w, "a roster with this name already exists", http.StatusConflict)
		return
	}
	if err := attempt(r, q, func() error {
		_, err := q.Exec(r.Context(), `UPDATE on_call_rosters SET name=$3, active=$4, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tc.TenantID, id, name, active)
		return err
	}); err != nil {
		if conflict(w, err, "a roster with this name already exists") {
			return
		}
		http.Error(w, "failed to update the roster", http.StatusInternalServerError)
		return
	}
	h.record(r, auditdomain.ResourceOnCallRoster, id, auditdomain.ActionRosterUpdated, map[string]any{"name": name, "active": active})
	write(w, http.StatusOK, map[string]any{"id": id, "name": name, "active": active})
}

type periodItem struct {
	ID       uuid.UUID `json:"id"`
	UserID   uuid.UUID `json:"user_id"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Capacity int       `json:"capacity"`
}

// ListPeriods: GET /on-call/rosters/{roster_id}/periods[?from=&to=] (default: from now to +30 days).
func (h *Handler) ListPeriods(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.read")
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "roster_id")
	if !ok {
		return
	}
	from, to := time.Now().Add(-24*time.Hour), time.Now().Add(30*24*time.Hour)
	for k, dst := range map[string]*time.Time{"from": &from, "to": &to} {
		if raw := r.URL.Query().Get(k); raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				http.Error(w, k+" must be RFC 3339", http.StatusBadRequest)
				return
			}
			*dst = t
		}
	}
	if !h.exists(r, q, `SELECT EXISTS(SELECT 1 FROM on_call_rosters WHERE tenant_id=$1 AND id=$2)`, tc.TenantID, id) {
		http.Error(w, "roster not found", http.StatusNotFound)
		return
	}
	rows, err := q.Query(r.Context(), `SELECT id, user_id, starts_at, ends_at, capacity FROM on_call_periods WHERE tenant_id=$1 AND roster_id=$2 AND ends_at > $3 AND starts_at < $4 ORDER BY starts_at, id LIMIT 1000`, tc.TenantID, id, from, to)
	if err != nil {
		http.Error(w, "failed to list periods", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := []periodItem{}
	for rows.Next() {
		var it periodItem
		if err := rows.Scan(&it.ID, &it.UserID, &it.From, &it.To, &it.Capacity); err != nil {
			http.Error(w, "failed to read periods", http.StatusInternalServerError)
			return
		}
		items = append(items, it)
	}
	write(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// AddPeriod puts an attendant on call for [from, to). The same person cannot hold two overlapping periods of one roster (409), the
// roster row is locked so two requests cannot both pass the check, and only an active attendant of this company can be scheduled.
func (h *Handler) AddPeriod(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "roster_id")
	if !ok {
		return
	}
	var in struct {
		UserID   uuid.UUID `json:"user_id"`
		From     time.Time `json:"from"`
		To       time.Time `json:"to"`
		Capacity int       `json:"capacity"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Capacity == 0 {
		in.Capacity = 1
	}
	if in.UserID == uuid.Nil || in.From.IsZero() || in.To.IsZero() || !in.To.After(in.From) || in.To.Sub(in.From) > 31*24*time.Hour || in.Capacity < 1 || in.Capacity > 100 {
		http.Error(w, "user_id, from and to (RFC 3339, to after from, at most 31 days) are required; capacity 1 to 100", http.StatusBadRequest)
		return
	}
	var locked bool
	err := q.QueryRow(r.Context(), `SELECT true FROM on_call_rosters WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tc.TenantID, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "roster not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to add the period", http.StatusInternalServerError)
		return
	}
	if !h.isAttendant(r, q, tc.TenantID, in.UserID) {
		http.Error(w, "the person is not an active attendant of this company", http.StatusNotFound)
		return
	}
	if h.exists(r, q, `SELECT EXISTS(SELECT 1 FROM on_call_periods WHERE tenant_id=$1 AND roster_id=$2 AND user_id=$3 AND starts_at < $5 AND ends_at > $4)`, tc.TenantID, id, in.UserID, in.From, in.To) {
		http.Error(w, "this person already has an overlapping period in this roster", http.StatusConflict)
		return
	}
	pid := uuid.New()
	if _, err := q.Exec(r.Context(), `INSERT INTO on_call_periods (id, tenant_id, roster_id, user_id, starts_at, ends_at, capacity, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		pid, tc.TenantID, id, in.UserID, in.From, in.To, in.Capacity, tc.ActorID); err != nil {
		http.Error(w, "failed to add the period", http.StatusInternalServerError)
		return
	}
	h.record(r, auditdomain.ResourceOnCallRoster, id, auditdomain.ActionOnCallAdded, map[string]any{"period_id": pid.String(), "user_id": in.UserID.String()})
	write(w, http.StatusCreated, periodItem{ID: pid, UserID: in.UserID, From: in.From, To: in.To, Capacity: in.Capacity})
}

func (h *Handler) RemovePeriod(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "roster_id")
	if !ok {
		return
	}
	pid, ok := pathUUID(w, r, "period_id")
	if !ok {
		return
	}
	tag, err := q.Exec(r.Context(), `DELETE FROM on_call_periods WHERE tenant_id=$1 AND roster_id=$2 AND id=$3`, tc.TenantID, id, pid)
	if err != nil {
		http.Error(w, "failed to remove the period", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() != 1 {
		http.Error(w, "period not found", http.StatusNotFound)
		return
	}
	h.record(r, auditdomain.ResourceOnCallRoster, id, auditdomain.ActionOnCallRemoved, map[string]any{"period_id": pid.String()})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- eligibility snapshot

// QueueEligibility: GET /queues/{queue_id}/eligibility[?at=]. Read-only monitoring: who could receive an automatic assignment at that
// instant and, for everyone else, every reason why. The names are the company's own attendants (agent.read).
func (h *Handler) QueueEligibility(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.read")
	if !ok {
		return
	}
	queueID, ok := pathUUID(w, r, "queue_id")
	if !ok {
		return
	}
	var at time.Time
	if err := q.QueryRow(r.Context(), `SELECT now()`).Scan(&at); err != nil { // one clock: the database's (ADR-0044), not each process's
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("at")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			http.Error(w, "at must be RFC 3339", http.StatusBadRequest)
			return
		}
		at = t
	}
	snap, err := Snapshot(r.Context(), q, tc.TenantID, queueID, at, h.presence)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "queue not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to evaluate the queue", http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, snap)
}
