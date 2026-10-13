package adapters

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/scheduling"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Handler serves sectors, calendars and the availability of a queue. Permissions: agent.read to read, agent.manage to change (the
// same keys as the queues); a delegated (Hub) context never holds them (not delegable), so it gets 403 here.
type Handler struct {
	pool     *pgxpool.Pool
	audit    auditports.AuditEventRepository
	presence PresenceChecker // optional: without it a tenant that requires presence reads every agent as presence_unavailable (fail closed)
}

// WithPresence wires the Valkey presence lookup used by the eligibility snapshot.
func (h *Handler) WithPresence(p PresenceChecker) *Handler { h.presence = p; return h }

func NewHandler(p *pgxpool.Pool, a auditports.AuditEventRepository) *Handler {
	return &Handler{pool: p, audit: a}
}

const nameMax = 60

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, permission string) (*tenancydomain.TenantContext, platformdb.Querier, bool) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return nil, nil, false
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	ok, err := tenancyadapters.ActorHasPermission(r.Context(), q, tc.TenantID, tc.ActorID, permission)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return nil, nil, false
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, nil, false
	}
	return tc, q, true
}

// maxBody bounds every JSON body (a calendar with all its holidays is far below it). Decoding stops at the limit, so a hostile payload
// never gets to be materialized.
const maxBody = 256 << 10

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid body", http.StatusBadRequest)
		}
		return false
	}
	return true
}

// attempt runs one statement that may violate a unique constraint inside a savepoint: on failure only the savepoint is rolled back, so the
// request's transaction stays usable and the handler can answer 409 instead of leaving an aborted transaction to be committed.
func attempt(r *http.Request, q platformdb.Querier, run func() error) error {
	if _, err := q.Exec(r.Context(), `SAVEPOINT attempt`); err != nil {
		return err
	}
	if err := run(); err != nil {
		_, _ = q.Exec(r.Context(), `ROLLBACK TO SAVEPOINT attempt`)
		return err
	}
	_, err := q.Exec(r.Context(), `RELEASE SAVEPOINT attempt`)
	return err
}

// conflict maps a unique violation (two requests raced past the pre-check) to the same 409 the pre-check gives.
func conflict(w http.ResponseWriter, err error, msg string) bool {
	if pgCode(err) == "23505" {
		http.Error(w, msg, http.StatusConflict)
		return true
	}
	return false
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// replan keeps the timers of waiting conversations true to the calendar that was just changed, in the same transaction.
func (h *Handler) replan(w http.ResponseWriter, r *http.Request, q platformdb.Querier, tenant uuid.UUID) bool {
	if _, err := ReplanWaiting(r.Context(), q, tenant); err != nil {
		http.Error(w, "failed to re-plan the waiting conversations", http.StatusInternalServerError)
		return false
	}
	return true
}

func (h *Handler) record(r *http.Request, res auditdomain.ResourceType, id uuid.UUID, action auditdomain.AuditAction, meta map[string]any) {
	if h.audit == nil {
		return
	}
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil {
		return
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, action, res, id, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	for k, v := range meta {
		ev.SetMetadata(k, v)
	}
	_ = h.audit.Store(r.Context(), ev)
}

func cleanName(raw string) (string, bool) {
	n := strings.TrimSpace(raw)
	c := utf8.RuneCountInString(n)
	return n, c >= 1 && c <= nameMax
}

func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		http.Error(w, "invalid "+name, http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func pgCode(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code
	}
	return ""
}

// ---------------------------------------------------------------- sectors

type sectorItem struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Active     bool       `json:"active"`
	CalendarID *uuid.UUID `json:"calendar_id,omitempty"`
	Queues     []sectorQ  `json:"queues"`
}

type sectorQ struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

func (h *Handler) ListSectors(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.read")
	if !ok {
		return
	}
	rows, err := q.Query(r.Context(), `SELECT s.id, s.name, s.active, s.calendar_id,
		COALESCE((SELECT json_agg(json_build_object('id', qq.id, 'name', qq.name) ORDER BY qq.name) FROM queues qq WHERE qq.tenant_id=s.tenant_id AND qq.sector_id=s.id), '[]'::json)
		FROM sectors s WHERE s.tenant_id=$1 ORDER BY s.active DESC, s.name`, tc.TenantID)
	if err != nil {
		http.Error(w, "failed to list sectors", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := []sectorItem{}
	for rows.Next() {
		var it sectorItem
		var qs []byte
		if err := rows.Scan(&it.ID, &it.Name, &it.Active, &it.CalendarID, &qs); err != nil || json.Unmarshal(qs, &it.Queues) != nil {
			http.Error(w, "failed to read sectors", http.StatusInternalServerError)
			return
		}
		items = append(items, it)
	}
	if rows.Err() != nil {
		http.Error(w, "failed to read sectors", http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (h *Handler) CreateSector(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name, valid := cleanName(req.Name)
	if !valid {
		http.Error(w, "name must have 1 to 60 characters", http.StatusBadRequest)
		return
	}
	var taken bool
	if err := q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sectors WHERE tenant_id=$1 AND name=$2)`, tc.TenantID, name).Scan(&taken); err != nil {
		http.Error(w, "failed to create sector", http.StatusInternalServerError)
		return
	}
	if taken {
		http.Error(w, "a sector with this name already exists", http.StatusConflict)
		return
	}
	id := uuid.New()
	if err := attempt(r, q, func() error {
		_, err := q.Exec(r.Context(), `INSERT INTO sectors (id, tenant_id, name) VALUES ($1,$2,$3)`, id, tc.TenantID, name)
		return err
	}); err != nil {
		if conflict(w, err, "a sector with this name already exists") {
			return
		}
		http.Error(w, "failed to create sector", http.StatusInternalServerError)
		return
	}
	h.record(r, auditdomain.ResourceSector, id, auditdomain.ActionSectorCreated, map[string]any{"name": name})
	write(w, http.StatusCreated, sectorItem{ID: id, Name: name, Active: true, Queues: []sectorQ{}})
}

// UpdateSector changes name, active and/or calendar_id (null clears the calendar: the sector inherits the tenant default again).
func (h *Handler) UpdateSector(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "sector_id")
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body) == 0 {
		http.Error(w, "name, active or calendar_id required", http.StatusBadRequest)
		return
	}
	var curName string
	var curActive bool
	var curCal *uuid.UUID
	err := q.QueryRow(r.Context(), `SELECT name, active, calendar_id FROM sectors WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tc.TenantID, id).Scan(&curName, &curActive, &curCal)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "sector not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read sector", http.StatusInternalServerError)
		return
	}
	name, active, cal := curName, curActive, curCal
	if raw, has := body["name"]; has {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			http.Error(w, "invalid name", http.StatusBadRequest)
			return
		}
		n, valid := cleanName(s)
		if !valid {
			http.Error(w, "name must have 1 to 60 characters", http.StatusBadRequest)
			return
		}
		name = n
	}
	if raw, has := body["active"]; has && json.Unmarshal(raw, &active) != nil {
		http.Error(w, "invalid active", http.StatusBadRequest)
		return
	}
	if raw, has := body["calendar_id"]; has {
		if string(raw) == "null" {
			cal = nil
		} else {
			var c uuid.UUID
			if json.Unmarshal(raw, &c) != nil {
				http.Error(w, "invalid calendar_id", http.StatusBadRequest)
				return
			}
			var exists bool
			if q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM calendars WHERE tenant_id=$1 AND id=$2)`, tc.TenantID, c).Scan(&exists) != nil || !exists {
				http.Error(w, "calendar not found", http.StatusNotFound)
				return
			}
			cal = &c
		}
	}
	if name != curName {
		var taken bool
		if q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sectors WHERE tenant_id=$1 AND name=$2 AND id<>$3)`, tc.TenantID, name, id).Scan(&taken) != nil || taken {
			http.Error(w, "a sector with this name already exists", http.StatusConflict)
			return
		}
	}
	if err := attempt(r, q, func() error {
		_, err := q.Exec(r.Context(), `UPDATE sectors SET name=$3, active=$4, calendar_id=$5, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tc.TenantID, id, name, active, cal)
		return err
	}); err != nil {
		if conflict(w, err, "a sector with this name already exists") {
			return
		}
		http.Error(w, "failed to update sector", http.StatusInternalServerError)
		return
	}
	if !h.replan(w, r, q, tc.TenantID) {
		return
	}
	h.record(r, auditdomain.ResourceSector, id, auditdomain.ActionSectorUpdated, map[string]any{"name": name, "active": active})
	write(w, http.StatusOK, map[string]any{"id": id, "name": name, "active": active, "calendar_id": cal})
}

// ---------------------------------------------------------------- calendars

type calendarListItem struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Timezone  string    `json:"timezone"`
	Revision  int       `json:"revision"`
	InUse     int       `json:"in_use"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (h *Handler) ListCalendars(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.read")
	if !ok {
		return
	}
	rows, err := q.Query(r.Context(), `SELECT c.id, c.name, c.kind, c.timezone, c.revision, c.updated_at,
		(SELECT count(*) FROM sectors s WHERE s.tenant_id=c.tenant_id AND s.calendar_id=c.id)
		+ (SELECT count(*) FROM queues qq WHERE qq.tenant_id=c.tenant_id AND qq.calendar_id=c.id)
		+ (SELECT count(*) FROM tenants t WHERE t.id=c.tenant_id AND t.default_calendar_id=c.id)
		FROM calendars c WHERE c.tenant_id=$1 ORDER BY c.name`, tc.TenantID)
	if err != nil {
		http.Error(w, "failed to list calendars", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := []calendarListItem{}
	for rows.Next() {
		var it calendarListItem
		if err := rows.Scan(&it.ID, &it.Name, &it.Kind, &it.Timezone, &it.Revision, &it.UpdatedAt, &it.InUse); err != nil {
			http.Error(w, "failed to read calendars", http.StatusInternalServerError)
			return
		}
		items = append(items, it)
	}
	if rows.Err() != nil {
		http.Error(w, "failed to read calendars", http.StatusInternalServerError)
		return
	}
	var def *uuid.UUID
	if err := q.QueryRow(r.Context(), `SELECT default_calendar_id FROM tenants WHERE id=$1`, tc.TenantID).Scan(&def); err != nil {
		http.Error(w, "failed to read calendars", http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{"items": items, "count": len(items), "default_timezone": scheduling.DefaultTimezone, "default_calendar_id": def})
}

func (h *Handler) GetCalendar(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.read")
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "calendar_id")
	if !ok {
		return
	}
	c, err := LoadCalendar(r.Context(), q, tc.TenantID, id)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "calendar not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read calendar", http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, c)
}

type calendarInput struct {
	Name       string          `json:"name"`
	Definition scheduling.Spec `json:"definition"`
	// ExpectedRevision, when sent, makes the save conditional: if somebody saved in between, the answer is 409 and nothing is lost.
	ExpectedRevision *int `json:"expected_revision,omitempty"`
}

func decodeCalendar(w http.ResponseWriter, r *http.Request) (string, scheduling.Spec, *int, bool) {
	var in calendarInput
	if !decodeJSON(w, r, &in) {
		return "", scheduling.Spec{}, nil, false
	}
	name, valid := cleanName(in.Name)
	if !valid {
		http.Error(w, "name must have 1 to 60 characters", http.StatusBadRequest)
		return "", scheduling.Spec{}, nil, false
	}
	if _, err := in.Definition.Schedule(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest) // the message names the rule that failed; it carries no tenant data
		return "", scheduling.Spec{}, nil, false
	}
	return name, in.Definition, in.ExpectedRevision, true
}

func (h *Handler) CreateCalendar(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	name, spec, _, ok := decodeCalendar(w, r)
	if !ok {
		return
	}
	var taken bool
	if q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM calendars WHERE tenant_id=$1 AND name=$2)`, tc.TenantID, name).Scan(&taken) != nil || taken {
		http.Error(w, "a calendar with this name already exists", http.StatusConflict)
		return
	}
	var id uuid.UUID
	err := attempt(r, q, func() (e error) {
		id, e = CreateCalendar(r.Context(), q, tc.TenantID, name, spec)
		return e
	})
	if err != nil {
		if conflict(w, err, "a calendar with this name already exists") {
			return
		}
		http.Error(w, "failed to create calendar", http.StatusInternalServerError)
		return
	}
	h.record(r, auditdomain.ResourceCalendar, id, auditdomain.ActionCalendarCreated, map[string]any{"name": name, "kind": string(spec.Kind), "timezone": spec.Timezone})
	c, err := LoadCalendar(r.Context(), q, tc.TenantID, id)
	if err != nil {
		http.Error(w, "failed to read calendar", http.StatusInternalServerError)
		return
	}
	write(w, http.StatusCreated, c)
}

func (h *Handler) UpdateCalendar(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "calendar_id")
	if !ok {
		return
	}
	name, spec, expected, ok := decodeCalendar(w, r)
	if !ok {
		return
	}
	var taken bool
	if q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM calendars WHERE tenant_id=$1 AND name=$2 AND id<>$3)`, tc.TenantID, name, id).Scan(&taken) != nil || taken {
		http.Error(w, "a calendar with this name already exists", http.StatusConflict)
		return
	}
	var rev int
	err := attempt(r, q, func() (e error) {
		rev, e = SaveDefinition(r.Context(), q, tc.TenantID, id, name, spec, expected)
		return e
	})
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "calendar not found", http.StatusNotFound)
		return
	}
	if errors.Is(err, ErrStale) {
		http.Error(w, "the calendar was changed by someone else; reload it and apply your change again", http.StatusConflict)
		return
	}
	if conflict(w, err, "a calendar with this name already exists") {
		return
	}
	if err != nil {
		http.Error(w, "failed to update calendar", http.StatusInternalServerError)
		return
	}
	if !h.replan(w, r, q, tc.TenantID) {
		return
	}
	h.record(r, auditdomain.ResourceCalendar, id, auditdomain.ActionCalendarUpdated, map[string]any{"name": name, "revision": rev})
	c, err := LoadCalendar(r.Context(), q, tc.TenantID, id)
	if err != nil {
		http.Error(w, "failed to read calendar", http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, c)
}

func (h *Handler) DeleteCalendar(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "calendar_id")
	if !ok {
		return
	}
	var inUse int
	if err := q.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM sectors WHERE tenant_id=$1 AND calendar_id=$2) + (SELECT count(*) FROM queues WHERE tenant_id=$1 AND calendar_id=$2)
		+ (SELECT count(*) FROM tenants WHERE id=$1 AND default_calendar_id=$2)`, tc.TenantID, id).Scan(&inUse); err != nil {
		http.Error(w, "failed to read calendar", http.StatusInternalServerError)
		return
	}
	if inUse > 0 {
		http.Error(w, "the calendar is still used by a sector, a queue or as the tenant default", http.StatusConflict)
		return
	}
	tag, err := q.Exec(r.Context(), `DELETE FROM calendars WHERE tenant_id=$1 AND id=$2`, tc.TenantID, id)
	if err != nil {
		if pgCode(err) == "23503" {
			http.Error(w, "the calendar is still in use", http.StatusConflict)
			return
		}
		http.Error(w, "failed to delete calendar", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() != 1 {
		http.Error(w, "calendar not found", http.StatusNotFound)
		return
	}
	h.record(r, auditdomain.ResourceCalendar, id, auditdomain.ActionCalendarDeleted, nil)
	w.WriteHeader(http.StatusNoContent)
}

// AddSuspension closes the calendar between two instants (emergency). It bumps the revision so derived timers are recomputed.
func (h *Handler) AddSuspension(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "calendar_id")
	if !ok {
		return
	}
	var in struct {
		From   time.Time `json:"from"`
		To     time.Time `json:"to"`
		Reason string    `json:"reason"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.From.IsZero() || in.To.IsZero() || !in.To.After(in.From) || len(in.Reason) > 200 {
		http.Error(w, "from and to (RFC 3339, to after from) are required; reason up to 200 characters", http.StatusBadRequest)
		return
	}
	// lock the calendar first (this serializes the count below), count, and only then bump the revision: a refused request changes nothing
	var locked bool
	err := q.QueryRow(r.Context(), `SELECT true FROM calendars WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tc.TenantID, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "calendar not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to suspend", http.StatusInternalServerError)
		return
	}
	var n int
	if err := q.QueryRow(r.Context(), `SELECT count(*) FROM calendar_suspensions WHERE tenant_id=$1 AND calendar_id=$2 AND ends_at > now() - interval '30 days'`, tc.TenantID, id).Scan(&n); err != nil {
		http.Error(w, "failed to suspend", http.StatusInternalServerError)
		return
	}
	if n >= 200 {
		http.Error(w, "too many suspensions on this calendar", http.StatusConflict)
		return
	}
	var rev int
	if err := q.QueryRow(r.Context(), `UPDATE calendars SET revision=revision+1, updated_at=now() WHERE tenant_id=$1 AND id=$2 RETURNING revision`, tc.TenantID, id).Scan(&rev); err != nil {
		http.Error(w, "failed to suspend", http.StatusInternalServerError)
		return
	}
	sid := uuid.New()
	if _, err := q.Exec(r.Context(), `INSERT INTO calendar_suspensions (id, tenant_id, calendar_id, starts_at, ends_at, reason, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		sid, tc.TenantID, id, in.From, in.To, strings.TrimSpace(in.Reason), tc.ActorID); err != nil {
		http.Error(w, "failed to suspend", http.StatusInternalServerError)
		return
	}
	if !h.replan(w, r, q, tc.TenantID) {
		return
	}
	h.record(r, auditdomain.ResourceCalendar, id, auditdomain.ActionCalendarSuspended, map[string]any{"suspension_id": sid.String(), "revision": rev})
	write(w, http.StatusCreated, Suspension{ID: sid, From: in.From, To: in.To, Reason: strings.TrimSpace(in.Reason)})
}

func (h *Handler) RemoveSuspension(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "calendar_id")
	if !ok {
		return
	}
	sid, ok := pathUUID(w, r, "suspension_id")
	if !ok {
		return
	}
	tag, err := q.Exec(r.Context(), `DELETE FROM calendar_suspensions WHERE tenant_id=$1 AND calendar_id=$2 AND id=$3`, tc.TenantID, id, sid)
	if err != nil {
		http.Error(w, "failed to resume", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() != 1 {
		http.Error(w, "suspension not found", http.StatusNotFound)
		return
	}
	if _, err := q.Exec(r.Context(), `UPDATE calendars SET revision=revision+1, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tc.TenantID, id); err != nil {
		http.Error(w, "failed to resume", http.StatusInternalServerError)
		return
	}
	if !h.replan(w, r, q, tc.TenantID) {
		return
	}
	h.record(r, auditdomain.ResourceCalendar, id, auditdomain.ActionCalendarResumed, map[string]any{"suspension_id": sid.String()})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- links

func readID(w http.ResponseWriter, r *http.Request, field string) (*uuid.UUID, bool) {
	var body map[string]*uuid.UUID
	if !decodeJSON(w, r, &body) {
		return nil, false
	}
	v, has := body[field]
	if !has {
		http.Error(w, field+" is required (null clears it)", http.StatusBadRequest)
		return nil, false
	}
	return v, true
}

// SetDefaultCalendar: PUT /settings/default-calendar {"calendar_id": uuid|null}.
func (h *Handler) SetDefaultCalendar(w http.ResponseWriter, r *http.Request) {
	// the default lives on the tenants row, which only a tenant administrator may update (tenant.manage names that power)
	tc, q, ok := h.authorize(w, r, "tenant.manage")
	if !ok {
		return
	}
	cal, ok := readID(w, r, "calendar_id")
	if !ok {
		return
	}
	if cal != nil && !h.exists(r, q, `SELECT EXISTS(SELECT 1 FROM calendars WHERE tenant_id=$1 AND id=$2)`, tc.TenantID, *cal) {
		http.Error(w, "calendar not found", http.StatusNotFound)
		return
	}
	tag, err := q.Exec(r.Context(), `UPDATE tenants SET default_calendar_id=$2, updated_at=now() WHERE id=$1`, tc.TenantID, cal)
	if err != nil {
		http.Error(w, "failed to set the default calendar", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() != 1 { // the row policy filtered the update out: never answer success (nor audit) for a change that did not happen
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !h.replan(w, r, q, tc.TenantID) {
		return
	}
	h.record(r, auditdomain.ResourceTenant, tc.TenantID, auditdomain.ActionTenantDefaultCalendar, map[string]any{"calendar_id": idText(cal)})
	write(w, http.StatusOK, map[string]any{"calendar_id": cal})
}

// SetQueueSector: PUT /queues/{queue_id}/sector {"sector_id": uuid|null}.
func (h *Handler) SetQueueSector(w http.ResponseWriter, r *http.Request) {
	h.setQueueLink(w, r, "sector_id", "sectors", auditdomain.ActionQueueSectorChanged)
}

// SetQueueCalendar: PUT /queues/{queue_id}/calendar {"calendar_id": uuid|null}.
func (h *Handler) SetQueueCalendar(w http.ResponseWriter, r *http.Request) {
	h.setQueueLink(w, r, "calendar_id", "calendars", auditdomain.ActionQueueCalendarChanged)
}

func (h *Handler) setQueueLink(w http.ResponseWriter, r *http.Request, field, table string, action auditdomain.AuditAction) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	queueID, ok := pathUUID(w, r, "queue_id")
	if !ok {
		return
	}
	target, ok := readID(w, r, field)
	if !ok {
		return
	}
	if target != nil && !h.exists(r, q, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE tenant_id=$1 AND id=$2)`, tc.TenantID, *target) {
		http.Error(w, strings.TrimSuffix(table, "s")+" not found", http.StatusNotFound)
		return
	}
	var prev *uuid.UUID
	col := field // column names equal the field names
	if err := q.QueryRow(r.Context(), `SELECT `+col+` FROM queues WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tc.TenantID, queueID).Scan(&prev); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "queue not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to read queue", http.StatusInternalServerError)
		return
	}
	if _, err := q.Exec(r.Context(), `UPDATE queues SET `+col+`=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tc.TenantID, queueID, target); err != nil {
		http.Error(w, "failed to update queue", http.StatusInternalServerError)
		return
	}
	if !h.replan(w, r, q, tc.TenantID) {
		return
	}
	h.record(r, auditdomain.ResourceQueue, queueID, action, map[string]any{"previous": idText(prev), "current": idText(target)})
	write(w, http.StatusOK, map[string]any{"queue_id": queueID, field: target})
}

func (h *Handler) exists(r *http.Request, q platformdb.Querier, sql string, args ...any) bool {
	var ok bool
	return q.QueryRow(r.Context(), sql, args...).Scan(&ok) == nil && ok
}

func idText(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// ---------------------------------------------------------------- availability

// QueueAvailability: GET /queues/{queue_id}/availability[?at=RFC3339]. Any member may ask (the agent.read key): it says whether the queue
// attends at that instant and when that changes, never the rules themselves.
func (h *Handler) QueueAvailability(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.read")
	if !ok {
		return
	}
	queueID, ok := pathUUID(w, r, "queue_id")
	if !ok {
		return
	}
	at := time.Now()
	if raw := r.URL.Query().Get("at"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			http.Error(w, "at must be RFC 3339", http.StatusBadRequest)
			return
		}
		at = t
	}
	a, err := QueueAvailability(r.Context(), q, tc.TenantID, queueID, at)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "queue not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to compute availability", http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, a)
}

// ---------------------------------------------------------------- closed notice

const maxNoticeRunes = 500

// GetClosedNotice returns the message the customer gets when this group is closed ("" = none).
func (h *Handler) GetClosedNotice(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.read")
	if !ok {
		return
	}
	queueID, ok := pathUUID(w, r, "queue_id")
	if !ok {
		return
	}
	var text string
	if err := q.QueryRow(r.Context(), `SELECT closed_notice FROM queues WHERE tenant_id=$1 AND id=$2`, tc.TenantID, queueID).Scan(&text); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "queue not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to read queue", http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{"queue_id": queueID, "text": text})
}

// PutClosedNotice sets (or, with empty text, removes) the closed notice. It is plain text sent to the customer by the routing engine, once per
// episode; {{proxima_abertura}} is replaced by the next opening in the calendar's timezone.
func (h *Handler) PutClosedNotice(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	queueID, ok := pathUUID(w, r, "queue_id")
	if !ok {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	text := strings.TrimSpace(in.Text)
	if len([]rune(text)) > maxNoticeRunes {
		http.Error(w, "the notice is limited to 500 characters", http.StatusBadRequest)
		return
	}
	for _, c := range text {
		if c < 0x20 && c != '\n' {
			http.Error(w, "the notice must be plain text", http.StatusBadRequest)
			return
		}
	}
	tag, err := q.Exec(r.Context(), `UPDATE queues SET closed_notice=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tc.TenantID, queueID, text)
	if err != nil {
		http.Error(w, "failed to update queue", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "queue not found", http.StatusNotFound)
		return
	}
	h.record(r, auditdomain.ResourceQueue, queueID, auditdomain.ActionClosedNoticeSaved, map[string]any{"has_text": text != ""})
	write(w, http.StatusOK, map[string]any{"queue_id": queueID, "text": text})
}
