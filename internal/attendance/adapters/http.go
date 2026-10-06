package adapters

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/attendance/application"
	"github.com/omnira/omnira/internal/attendance/domain"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

const maxBody = 64 << 10

// Handler is the HTTP surface of ADR-0020. The tenant always comes from the TenantContext (the authenticated session),
// never from the path or the body; permissions are re-checked by the service from the role matrix.
type Handler struct{ svc *application.Service }

func NewHandler(svc *application.Service) *Handler { return &Handler{svc: svc} }

type Registrar interface {
	Handle(pattern string, handler http.Handler)
}

// Routes mounts the API. wrap adds authn + the tenant session (see httpserver.RegisterAttendanceHandlers).
func (h *Handler) Routes(mux Registrar, wrap func(http.HandlerFunc) http.Handler) {
	const base = "/api/v1/tenants/{tenant_id}"
	mux.Handle("POST "+base+"/inbox/conversations/{conversation_id}/finalize", wrap(h.finalize))
	mux.Handle("POST "+base+"/inbox/conversations/{conversation_id}/finalize/suggest", wrap(h.suggest))
	mux.Handle("GET "+base+"/inbox/conversations/{conversation_id}/attendance-context", wrap(h.contextOfConversation))
	mux.Handle("GET "+base+"/inbox/conversations/{conversation_id}/history-search", wrap(h.searchHistory))
	mux.Handle("GET "+base+"/contacts/{contact_id}/attendance-history", wrap(h.historyOfContact))
	mux.Handle("POST "+base+"/follow-ups/{follow_up_id}/resolve", wrap(h.resolveFollowUp))
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

// fail maps domain errors to HTTP. Unknown errors are a plain 500 with nothing internal leaked.
func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, domain.ErrForbidden):
		writeErr(w, http.StatusForbidden, "forbidden", "you do not have permission to do this")
	case errors.Is(err, domain.ErrNotAContact):
		writeErr(w, http.StatusUnprocessableEntity, "not_a_contact_conversation", "only a conversation with a contact can be finalized")
	case errors.Is(err, domain.ErrSuggestionDisabled):
		writeErr(w, http.StatusServiceUnavailable, "ai_disabled", "the AI suggestion is not enabled")
	case errors.Is(err, domain.ErrSuggestionUnavailable):
		writeErr(w, http.StatusServiceUnavailable, "ai_unavailable", "the AI suggestion is not available right now")
	case errors.Is(err, domain.ErrNothingToSuggest):
		writeErr(w, http.StatusUnprocessableEntity, "nothing_to_suggest", "there is nothing in the conversation to summarize")
	case errors.Is(err, domain.ErrAlreadyHandled):
		writeErr(w, http.StatusConflict, "already_resolved", "the item was already resolved")
	case errors.Is(err, domain.ErrInvalid):
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal", "unexpected error")
	}
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
	dec.DisallowUnknownFields() // a tenant_id (or anything else) smuggled into the body is an error, not an override
	if err := dec.Decode(dst); err != nil || dec.More() {
		writeErr(w, http.StatusBadRequest, "invalid", "malformed or unknown fields in the request body")
		return false
	}
	return true
}

func authenticated(w http.ResponseWriter, r *http.Request) bool {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return false
	}
	return true
}

type followUpDTO struct {
	ID             uuid.UUID  `json:"id"`
	ConversationID uuid.UUID  `json:"conversation_id"`
	Kind           string     `json:"kind"`
	Text           string     `json:"text"`
	OwnerUserID    *uuid.UUID `json:"owner_user_id"`
	DueAt          *time.Time `json:"due_at"`
	Status         string     `json:"status"`
	Truth          string     `json:"truth"`
	CreatedAt      time.Time  `json:"created_at"`
	ResolvedAt     *time.Time `json:"resolved_at"`
	ResolutionNote string     `json:"resolution_note"`
}

func followUps(in []domain.FollowUp) []followUpDTO {
	out := make([]followUpDTO, 0, len(in))
	for _, f := range in {
		out = append(out, followUpDTO{ID: f.ID, ConversationID: f.ConversationID, Kind: string(f.Kind), Text: f.Text, OwnerUserID: f.OwnerUserID,
			DueAt: f.DueAt, Status: string(f.Status), Truth: string(f.Truth), CreatedAt: f.CreatedAt, ResolvedAt: f.ResolvedAt, ResolutionNote: f.ResolutionNote})
	}
	return out
}

type closureDTO struct {
	ID                 uuid.UUID     `json:"id"`
	ConversationID     uuid.UUID     `json:"conversation_id"`
	ClosedByUserID     *uuid.UUID    `json:"closed_by_user_id"`
	Source             string        `json:"source"`
	Reason             string        `json:"reason"`
	Note               string        `json:"note"`
	Summary            string        `json:"summary"`
	SummaryTruth       string        `json:"summary_truth"`
	LocalTicketsClosed int           `json:"local_tickets_closed"`
	TicketsKept        int           `json:"tickets_kept"`
	CreatedAt          time.Time     `json:"created_at"`
	FollowUps          []followUpDTO `json:"follow_ups"`
}

func closure(c *domain.Closure, fu []domain.FollowUp) closureDTO {
	return closureDTO{ID: c.ID, ConversationID: c.ConversationID, ClosedByUserID: c.ClosedBy, Source: string(c.Source), Reason: string(c.Reason),
		Note: c.Note, Summary: c.Summary, SummaryTruth: string(c.SummaryTruth), LocalTicketsClosed: c.LocalTicketsClosed, TicketsKept: c.TicketsKept,
		CreatedAt: c.CreatedAt, FollowUps: followUps(fu)}
}

func (h *Handler) finalize(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	var in domain.FinalizeInput
	if !decode(w, r, &in) {
		return
	}
	in.ConversationID = id
	res, err := h.svc.Finalize(r.Context(), in)
	if err != nil {
		fail(w, err)
		return
	}
	body := map[string]any{"changed": res.Changed}
	if res.Closure != nil {
		body["closure"] = closure(res.Closure, res.FollowUps)
	}
	writeJSON(w, http.StatusOK, body)
}

type suggestionDTO struct {
	Summary      string `json:"summary"`
	SummaryTruth string `json:"summary_truth"`
	FollowUps    []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"follow_ups"`
	Model           string `json:"model"`
	BasedOnMessages int    `json:"based_on_messages"`
}

func (h *Handler) suggest(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	sg, err := h.svc.SuggestClosing(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	out := suggestionDTO{Summary: sg.Summary, SummaryTruth: string(domain.TruthAIInferred), Model: sg.Model, BasedOnMessages: sg.BasedOnMessages}
	out.FollowUps = make([]struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}, 0, len(sg.FollowUps))
	for _, f := range sg.FollowUps {
		out.FollowUps = append(out.FollowUps, struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}{string(f.Kind), f.Text})
	}
	writeJSON(w, http.StatusOK, out)
}

type historyDTO struct {
	ContactID     uuid.UUID     `json:"contact_id"`
	Attendances   []closureDTO  `json:"attendances"`
	OpenFollowUps []followUpDTO `json:"open_follow_ups"`
}

func historyBody(h application.History) historyDTO {
	out := historyDTO{ContactID: h.ContactID, Attendances: make([]closureDTO, 0, len(h.Attendances)), OpenFollowUps: followUps(h.OpenFollowUps)}
	for _, a := range h.Attendances {
		c := a.Closure
		out.Attendances = append(out.Attendances, closure(&c, a.FollowUps))
	}
	return out
}

func limitQuery(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return n
}

func (h *Handler) contextOfConversation(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	hist, err := h.svc.HistoryOfConversation(r.Context(), id, limitQuery(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, historyBody(hist))
}

func (h *Handler) historyOfContact(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "contact_id")
	if !ok {
		return
	}
	hist, err := h.svc.HistoryOfContact(r.Context(), id, limitQuery(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, historyBody(hist))
}

type hitDTO struct {
	At             time.Time `json:"at"`
	Role           string    `json:"role"`
	Snippet        string    `json:"snippet"`
	ConversationID uuid.UUID `json:"conversation_id"`
}

func (h *Handler) searchHistory(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	hits, err := h.svc.SearchHistoryOfConversation(r.Context(), id, r.URL.Query().Get("q"), limitQuery(r))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]hitDTO, 0, len(hits))
	for _, x := range hits {
		out = append(out, hitDTO{At: x.At, Role: x.Role, Snippet: x.Snippet, ConversationID: x.ConversationID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) resolveFollowUp(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	id, ok := pathUUID(w, r, "follow_up_id")
	if !ok {
		return
	}
	var in domain.ResolveFollowUpInput
	if !decode(w, r, &in) {
		return
	}
	item, err := h.svc.ResolveFollowUp(r.Context(), id, in)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, followUps([]domain.FollowUp{*item})[0])
}
