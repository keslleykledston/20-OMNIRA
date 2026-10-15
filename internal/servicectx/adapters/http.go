package adapters

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/application"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

const maxBody = 32 << 10

// Handler is the HTTP surface of Service Context & Operations. The tenant ALWAYS comes from the TenantContext (the authenticated session), never from the
// path or the body; the body refuses unknown fields so a smuggled tenant_id is an error, not an override.
type Handler struct {
	svc     *application.Service
	catalog *application.Catalog
	cases   *CasesHandler
}

func NewHandler(svc *application.Service, catalog *application.Catalog) *Handler {
	return &Handler{svc: svc, catalog: catalog, cases: &CasesHandler{svc: svc}}
}

// Registrar is the part of http.ServeMux the module needs (a test can record the routes).
type Registrar interface {
	Handle(pattern string, handler http.Handler)
}

// Routes mounts the API. wrap adds authn and the tenant session (see httpserver.RegisterServiceContextHandlers); delegable, when set, wraps a route for a Hub
// agent acting for the instance with the permission key that opens it (ADR-0040). A nil delegable keeps every route members-only.
func (h *Handler) Routes(mux Registrar, wrap func(http.HandlerFunc) http.Handler, delegable func(key string, next http.Handler) http.Handler) {
	const base = "/api/v1/tenants/{tenant_id}"
	route := func(key string, fn http.HandlerFunc) http.Handler {
		if delegable == nil || key == "" {
			return wrap(fn)
		}
		return delegable(key, wrap(fn))
	}
	mux.Handle("POST "+base+"/service-sessions", route(application.PermCreate, h.open))
	mux.Handle("GET "+base+"/service-sessions", route(application.PermRead, h.list))
	mux.Handle("GET "+base+"/service-sessions/{session_id}", route(application.PermRead, h.get))
	mux.Handle("PATCH "+base+"/service-sessions/{session_id}", route(application.PermUpdate, h.patch))
	mux.Handle("POST "+base+"/service-sessions/{session_id}/status", route(application.PermUpdate, h.status))
	mux.Handle("POST "+base+"/service-sessions/{session_id}/close", route(application.PermClose, h.close))
	mux.Handle("POST "+base+"/service-sessions/{session_id}/reopen", route(application.PermClose, h.reopen))
	mux.Handle("GET "+base+"/service-sessions/{session_id}/messages", route(application.PermRead, h.messages))
	mux.Handle("GET "+base+"/conversations/{conversation_id}/service-session", route(application.PermRead, h.forConversation))
	mux.Handle("GET "+base+"/service-sessions/{session_id}/timeline", route(application.PermRead, h.sessionTimeline))
	mux.Handle("GET "+base+"/contacts/{contact_id}/service-timeline", route(application.PermRead, h.contactTimeline))
	mux.Handle("GET "+base+"/contacts/{contact_id}/service-context", route(application.PermRead, h.contactContext))
	// catalogs: the company's own categories and result codes. READING them is delegable like the rest of the module (an agent acting for the company needs the
	// list to classify); CHANGING them is the company's configuration and is never delegable.
	mux.Handle("GET "+base+"/service-categories", route(application.PermRead, h.listCategories))
	mux.Handle("POST "+base+"/service-categories", wrap(h.createCategory))
	mux.Handle("PATCH "+base+"/service-categories/{category_id}", wrap(h.patchCategory))
	mux.Handle("GET "+base+"/service-result-codes", route(application.PermRead, h.listResultCodes))
	mux.Handle("POST "+base+"/service-result-codes", wrap(h.createResultCode))
	mux.Handle("PATCH "+base+"/service-result-codes/{code_id}", wrap(h.patchResultCode))
	if h.cases != nil {
		h.cases.Routes(mux, wrap, delegable)
	}
}

type apiError struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, apiError{Error: code, Detail: detail})
}

// fail maps the module's errors to HTTP. An unknown error is a plain 500 with nothing internal leaked.
func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, domain.ErrForbidden):
		writeErr(w, http.StatusForbidden, "forbidden", "you do not have permission to do this")
	case errors.Is(err, domain.ErrPreconditionNeeded):
		writeErr(w, http.StatusPreconditionRequired, "precondition_required", "send the version you read in If-Match")
	case errors.Is(err, domain.ErrConflict):
		writeErr(w, http.StatusPreconditionFailed, "version_conflict", "the record changed since you read it; read it again")
	case errors.Is(err, domain.ErrInvalidTransition):
		writeErr(w, http.StatusConflict, "invalid_transition", "that change is not allowed in the current state")
	case errors.Is(err, domain.ErrReopenNotAllowed):
		writeErr(w, http.StatusConflict, "reopen_not_allowed", "this record cannot be reopened")
	case errors.Is(err, domain.ErrConversationClosed):
		writeErr(w, http.StatusConflict, "conversation_closed", "the conversation is closed")
	case errors.Is(err, domain.ErrOutcomeUnknown):
		writeErr(w, http.StatusConflict, "outcome_unknown", "an earlier request has an unknown outcome and must be reconciled first")
	case errors.Is(err, domain.ErrIdempotencyMismatch):
		writeErr(w, http.StatusUnprocessableEntity, "idempotency_key_reused", "this Idempotency-Key was used with a different request")
	case errors.Is(err, domain.ErrNotACustomerChat):
		writeErr(w, http.StatusUnprocessableEntity, "not_a_customer_conversation", "only a conversation with a customer has service sessions")
	case errors.Is(err, domain.ErrUnknownResolution):
		writeErr(w, http.StatusUnprocessableEntity, "unknown_result", "the result code does not exist or is inactive")
	case errors.Is(err, domain.ErrNotConfigured):
		writeErr(w, http.StatusServiceUnavailable, "not_configured", "the integration is not configured for this company")
	case errors.Is(err, domain.ErrInvalid):
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal", "unexpected error")
	}
}

func authenticated(w http.ResponseWriter, r *http.Request) bool {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return false
	}
	return true
}

func pathUUID(w http.ResponseWriter, r *http.Request, key string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(key))
	if err != nil || id == uuid.Nil {
		writeErr(w, http.StatusBadRequest, "invalid", "invalid "+key)
		return uuid.Nil, false
	}
	return id, true
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.More() {
		writeErr(w, http.StatusBadRequest, "invalid", "malformed or unknown fields in the request body")
		return false
	}
	return true
}

// ifMatch reads the version the client saw: If-Match: "3" (or 3). Absent or unreadable is 0, which the service refuses as "precondition required".
func ifMatch(r *http.Request) int {
	v := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0
	}
	return n
}

func etag(version int) string { return `"` + strconv.Itoa(version) + `"` }

type sessionDTO struct {
	ID                  uuid.UUID  `json:"id"`
	Protocol            string     `json:"protocol"`
	ExternalProtocol    *string    `json:"external_protocol"`
	ConversationID      *uuid.UUID `json:"conversation_id"`
	ContactID           *uuid.UUID `json:"contact_id"`
	CustomerID          *uuid.UUID `json:"customer_id"`
	QueueID             *uuid.UUID `json:"queue_id"`
	CategoryID          *uuid.UUID `json:"category_id"`
	Status              string     `json:"status"`
	ResolutionCode      *string    `json:"resolution_code"`
	ResolutionCanonical *string    `json:"resolution_canonical"`
	ResolutionNote      string     `json:"resolution_note"`
	OriginChannel       string     `json:"origin_channel"`
	OpenedSource        string     `json:"opened_source"`
	ClosedSource        *string    `json:"closed_source"`
	OpenedBy            *uuid.UUID `json:"opened_by_user_id"`
	ClosedBy            *uuid.UUID `json:"closed_by_user_id"`
	AssignedTo          *uuid.UUID `json:"assigned_to_user_id"`
	OpenedAt            time.Time  `json:"opened_at"`
	FirstResponseAt     *time.Time `json:"first_response_at"`
	ClosedAt            *time.Time `json:"closed_at"`
	ReopenCount         int        `json:"reopen_count"`
	Version             int        `json:"version"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

func toDTO(s *domain.Session, assigned *uuid.UUID) sessionDTO {
	return sessionDTO{ID: s.ID, Protocol: s.Protocol, ExternalProtocol: s.ExternalProtocol, ConversationID: s.ConversationID, ContactID: s.ContactID,
		CustomerID: s.CustomerID, QueueID: s.QueueID, CategoryID: s.CategoryID, Status: string(s.Status), ResolutionCode: s.ResolutionCode,
		ResolutionCanonical: s.ResolutionCanonical, ResolutionNote: s.ResolutionNote, OriginChannel: s.OriginChannel, OpenedSource: s.OpenedSource,
		ClosedSource: s.ClosedSource, OpenedBy: s.OpenedBy, ClosedBy: s.ClosedBy, AssignedTo: assigned, OpenedAt: s.OpenedAt,
		FirstResponseAt: s.FirstResponseAt, ClosedAt: s.ClosedAt, ReopenCount: s.ReopenCount, Version: s.Version, UpdatedAt: s.UpdatedAt}
}

func writeSession(w http.ResponseWriter, status int, s *domain.Session, assigned *uuid.UUID) {
	w.Header().Set("ETag", etag(s.Version))
	writeJSON(w, status, toDTO(s, assigned))
}

type openBody struct {
	ConversationID   *uuid.UUID `json:"conversation_id"`
	ContactID        *uuid.UUID `json:"contact_id"`
	CustomerID       *uuid.UUID `json:"customer_id"`
	QueueID          *uuid.UUID `json:"queue_id"`
	CategoryID       *uuid.UUID `json:"category_id"`
	OriginChannel    string     `json:"origin_channel"`
	ExternalProtocol string     `json:"external_protocol"`
}

func (h *Handler) open(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	var b openBody
	if !decode(w, r, &b) {
		return
	}
	// the source is decided HERE from who is calling, never taken from the body: an API caller cannot pretend to be a flow or the finalization
	res, err := h.svc.Open(r.Context(), domain.OpenInput{ConversationID: b.ConversationID, ContactID: b.ContactID, CustomerID: b.CustomerID, QueueID: b.QueueID,
		CategoryID: b.CategoryID, OriginChannel: b.OriginChannel, ExternalProtocol: b.ExternalProtocol, IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")),
		Source: domain.SourceOperator})
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusCreated
	if !res.Created {
		status = http.StatusOK
	}
	if res.Replay {
		w.Header().Set("Idempotent-Replay", "true") // only when the Idempotency-Key was recognised, not whenever an existing session is returned
	}
	writeSession(w, status, res.Session, nil)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "session_id")
	if !ok {
		return
	}
	s, assigned, err := h.svc.Get(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeSession(w, http.StatusOK, s, assigned)
}

func (h *Handler) forConversation(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	s, assigned, err := h.svc.ForConversation(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if s == nil {
		writeErr(w, http.StatusNotFound, "no_session", "this conversation has no service session yet")
		return
	}
	writeSession(w, http.StatusOK, s, assigned)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	q := r.URL.Query()
	f := ports.SessionFilter{Status: domain.SessionStatus(q.Get("status")), Protocol: q.Get("protocol"), Cursor: q.Get("cursor")}
	for key, dst := range map[string]**uuid.UUID{"conversation_id": &f.ConversationID, "contact_id": &f.ContactID, "customer_id": &f.CustomerID, "queue_id": &f.QueueID} {
		if v := q.Get(key); v != "" {
			id, err := uuid.Parse(v)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "invalid", "invalid "+key)
				return
			}
			*dst = &id
		}
	}
	for key, dst := range map[string]**time.Time{"opened_from": &f.OpenedFrom, "opened_to": &f.OpenedTo} {
		if v := q.Get(key); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "invalid", "invalid "+key)
				return
			}
			*dst = &t
		}
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid", "invalid limit")
			return
		}
		f.Limit = n
	}
	items, next, err := h.svc.List(r.Context(), f)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]sessionDTO, 0, len(items))
	for i := range items {
		out = append(out, toDTO(&items[i], nil))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
}

type patchBody struct {
	CustomerID *uuid.UUID `json:"customer_id"`
	QueueID    *uuid.UUID `json:"queue_id"`
	CategoryID *uuid.UUID `json:"category_id"`
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "session_id")
	if !ok {
		return
	}
	var b patchBody
	if !decode(w, r, &b) {
		return
	}
	s, err := h.svc.Patch(r.Context(), id, domain.PatchInput{CustomerID: b.CustomerID, QueueID: b.QueueID, CategoryID: b.CategoryID}, ifMatch(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeSession(w, http.StatusOK, s, nil)
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "session_id")
	if !ok {
		return
	}
	var b struct {
		Status string `json:"status"`
	}
	if !decode(w, r, &b) {
		return
	}
	s, err := h.svc.SetStatus(r.Context(), id, domain.SessionStatus(b.Status), ifMatch(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeSession(w, http.StatusOK, s, nil)
}

func (h *Handler) close(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "session_id")
	if !ok {
		return
	}
	var b struct {
		ResolutionCode string `json:"resolution_code"`
		Note           string `json:"note"`
		Abandon        bool   `json:"abandon"`
	}
	if !decode(w, r, &b) {
		return
	}
	s, err := h.svc.Close(r.Context(), id, domain.CloseInput{ResolutionCode: b.ResolutionCode, Note: b.Note, Abandon: b.Abandon}, ifMatch(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeSession(w, http.StatusOK, s, nil)
}

func (h *Handler) reopen(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "session_id")
	if !ok {
		return
	}
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	s, err := h.svc.Reopen(r.Context(), id, domain.ReopenInput{Reason: b.Reason}, ifMatch(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeSession(w, http.StatusOK, s, nil)
}

type messageRefDTO struct {
	ID         uuid.UUID  `json:"id"`
	Direction  string     `json:"direction"`
	Type       string     `json:"type"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	SentByUser *uuid.UUID `json:"sent_by_user_id"`
}

func (h *Handler) messages(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "session_id")
	if !ok {
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid", "invalid limit")
			return
		}
		limit = n
	}
	items, next, err := h.svc.Messages(r.Context(), id, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]messageRefDTO, 0, len(items))
	for _, m := range items {
		out = append(out, messageRefDTO{ID: m.ID, Direction: m.Direction, Type: m.Type, Status: m.Status, CreatedAt: m.CreatedAt, SentByUser: m.SentByUser})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
}

// ---- catalogs ----

type categoryDTO struct {
	ID     uuid.UUID `json:"id"`
	Code   string    `json:"code"`
	Label  string    `json:"label"`
	Active bool      `json:"active"`
}

type resultCodeDTO struct {
	ID            uuid.UUID `json:"id"`
	Code          string    `json:"code"`
	CanonicalCode string    `json:"canonical_code"`
	Label         string    `json:"label"`
	Active        bool      `json:"active"`
}

func (h *Handler) listCategories(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	items, err := h.catalog.Categories(r.Context(), r.URL.Query().Get("include_inactive") == "true")
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]categoryDTO, 0, len(items))
	for _, c := range items {
		out = append(out, categoryDTO{c.ID, c.Code, c.Label, c.Active})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) createCategory(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	var b struct{ Code, Label string }
	var raw struct {
		Code  string `json:"code"`
		Label string `json:"label"`
	}
	if !decode(w, r, &raw) {
		return
	}
	b.Code, b.Label = raw.Code, raw.Label
	c, err := h.catalog.CreateCategory(r.Context(), b.Code, b.Label)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, categoryDTO{c.ID, c.Code, c.Label, c.Active})
}

type catalogPatch struct {
	Label  *string `json:"label"`
	Active *bool   `json:"active"`
}

func (h *Handler) patchCategory(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "category_id")
	if !ok {
		return
	}
	var b catalogPatch
	if !decode(w, r, &b) {
		return
	}
	c, err := h.catalog.UpdateCategory(r.Context(), id, b.Label, b.Active)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, categoryDTO{c.ID, c.Code, c.Label, c.Active})
}

func (h *Handler) listResultCodes(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	items, err := h.catalog.ResultCodes(r.Context(), r.URL.Query().Get("include_inactive") == "true")
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]resultCodeDTO, 0, len(items))
	for _, c := range items {
		out = append(out, resultCodeDTO{c.ID, c.Code, c.CanonicalCode, c.Label, c.Active})
	}
	// the built-in codes are always available; the company's own come from the table
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "built_in": domain.CanonicalResolutions()})
}

func (h *Handler) createResultCode(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	var b struct {
		Code          string `json:"code"`
		CanonicalCode string `json:"canonical_code"`
		Label         string `json:"label"`
	}
	if !decode(w, r, &b) {
		return
	}
	c, err := h.catalog.CreateResultCode(r.Context(), b.Code, b.CanonicalCode, b.Label)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, resultCodeDTO{c.ID, c.Code, c.CanonicalCode, c.Label, c.Active})
}

func (h *Handler) patchResultCode(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "code_id")
	if !ok {
		return
	}
	var b catalogPatch
	if !decode(w, r, &b) {
		return
	}
	c, err := h.catalog.UpdateResultCode(r.Context(), id, b.Label, b.Active)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resultCodeDTO{c.ID, c.Code, c.CanonicalCode, c.Label, c.Active})
}

// ---- timeline and context ----

type timelineItemDTO struct {
	ID             uuid.UUID      `json:"id"`
	EventType      string         `json:"event_type"`
	EntityType     string         `json:"entity_type"`
	EntityID       uuid.UUID      `json:"entity_id"`
	SessionID      *uuid.UUID     `json:"session_id"`
	CaseID         *uuid.UUID     `json:"case_id"`
	ConversationID *uuid.UUID     `json:"conversation_id"`
	ContactID      *uuid.UUID     `json:"contact_id"`
	CustomerID     *uuid.UUID     `json:"customer_id"`
	ActorType      string         `json:"actor_type"`
	ActorID        *uuid.UUID     `json:"actor_id"`
	SourceSystem   string         `json:"source_system"`
	OccurredAt     time.Time      `json:"occurred_at"`
	SchemaVersion  int            `json:"schema_version"`
	Payload        map[string]any `json:"payload"`
	Note           string         `json:"note"`
	CorrelationID  *uuid.UUID     `json:"correlation_id"`
	Derived        bool           `json:"derived"`
}

func toTimelineDTO(it ports.TimelineItem) timelineItemDTO {
	if it.Payload == nil {
		it.Payload = map[string]any{}
	}
	return timelineItemDTO{ID: it.ID, EventType: it.EventType, EntityType: it.EntityType, EntityID: it.EntityID, SessionID: it.SessionID, CaseID: it.CaseID,
		ConversationID: it.ConversationID, ContactID: it.ContactID, CustomerID: it.CustomerID, ActorType: it.ActorType, ActorID: it.ActorID,
		SourceSystem: it.SourceSystem, OccurredAt: it.OccurredAt, SchemaVersion: it.SchemaVersion, Payload: it.Payload, Note: it.Note,
		CorrelationID: it.CorrelationID, Derived: it.Derived}
}

// timelineParams reads limit, cursor and order (desc by default: newest first).
func timelineParams(w http.ResponseWriter, r *http.Request) (cursor string, limit int, ascending bool, ok bool) {
	q := r.URL.Query()
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid", "invalid limit")
			return "", 0, false, false
		}
		limit = n
	}
	switch q.Get("order") {
	case "", "desc":
	case "asc":
		ascending = true
	default:
		writeErr(w, http.StatusBadRequest, "invalid", "order must be asc or desc")
		return "", 0, false, false
	}
	return q.Get("cursor"), limit, ascending, true
}

func writeTimeline(w http.ResponseWriter, items []ports.TimelineItem, next string, err error) {
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]timelineItemDTO, 0, len(items))
	for _, it := range items {
		out = append(out, toTimelineDTO(it))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
}

func (h *Handler) sessionTimeline(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "session_id")
	if !ok {
		return
	}
	cursor, limit, asc, ok := timelineParams(w, r)
	if !ok {
		return
	}
	items, next, err := h.svc.SessionTimeline(r.Context(), id, cursor, limit, asc)
	writeTimeline(w, items, next, err)
}

func (h *Handler) contactTimeline(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "contact_id")
	if !ok {
		return
	}
	cursor, limit, asc, ok := timelineParams(w, r)
	if !ok {
		return
	}
	items, next, err := h.svc.ContactTimeline(r.Context(), id, cursor, limit, asc)
	writeTimeline(w, items, next, err)
}

func (h *Handler) contactContext(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "contact_id")
	if !ok {
		return
	}
	var customer *uuid.UUID
	if v := r.URL.Query().Get("customer_id"); v != "" {
		c, err := uuid.Parse(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid", "invalid customer_id")
			return
		}
		customer = &c
	}
	cc, err := h.svc.ContactContext(r.Context(), id, customer)
	if err != nil {
		fail(w, err)
		return
	}
	customers := make([]map[string]any, 0, len(cc.Customers))
	for _, c := range cc.Customers {
		customers = append(customers, map[string]any{"id": c.ID, "name": c.Name, "is_primary": c.IsPrimary})
	}
	recent := make([]sessionDTO, 0, len(cc.RecentSessions))
	for i := range cc.RecentSessions {
		recent = append(recent, toDTO(&cc.RecentSessions[i], nil))
	}
	events := make([]timelineItemDTO, 0, len(cc.RecentEvents))
	for _, ev := range cc.RecentEvents {
		events = append(events, toTimelineDTO(ev))
	}
	var active *sessionDTO
	if cc.ActiveSession != nil {
		d := toDTO(cc.ActiveSession, nil)
		active = &d
	}
	now := time.Now().UTC()
	cases := make([]caseDTO, 0, len(cc.OpenCases))
	for i := range cc.OpenCases {
		cases = append(cases, toCaseDTO(&cc.OpenCases[i]))
	}
	tasks := make([]taskDTO, 0, len(cc.PendingTasks))
	for i := range cc.PendingTasks {
		tasks = append(tasks, toTaskDTO(&cc.PendingTasks[i], now))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"open_cases": cases, "pending_tasks": tasks,
		"contact_id": cc.ContactID, "customers": customers, "needs_customer_selection": cc.NeedsSelection, "selected_customer_id": cc.SelectedCustomerID,
		"active_session": active, "recent_sessions": recent, "recent_events": events,
	})
}
