package conversationcontrol

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// TakeoverHandler is the supervisor's explicit takeover of a conversation an external application holds (ADR-0046 §6.5). It needs
// conversation.takeover_external, a written reason and a confirmation flag; it is never automatic after a conflict. The route names
// {conversation_id}, so the tenant authorization middleware has already refused internal-contact conversations to whoever may not read them.
type TakeoverHandler struct {
	pool    *pgxpool.Pool
	audit   auditports.AuditEventRepository
	service Service
}

func NewTakeoverHandler(pool *pgxpool.Pool, audit auditports.AuditEventRepository) *TakeoverHandler {
	return &TakeoverHandler{pool: pool, audit: audit}
}

type takeoverRequest struct {
	Reason  string `json:"reason"`
	Confirm bool   `json:"confirm"`
}

type takeoverResponse struct {
	ConversationID uuid.UUID `json:"conversation_id"`
	Kind           string    `json:"control_kind"`
	Version        int64     `json:"control_version"`
}

// Takeover: POST /tenants/{tenant_id}/inbox/conversations/{conversation_id}/takeover
func (h *TakeoverHandler) Takeover(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil || tc.Source != tenancydomain.AccessSourceDirect {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	ok, err := tenancyadapters.ActorHasPermission(r.Context(), q, tc.TenantID, tc.ActorID, "conversation.takeover_external")
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	conversationID, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return
	}
	var req takeoverRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) || !req.Confirm {
		http.Error(w, "confirm must be true", http.StatusBadRequest)
		return
	}
	st, err := h.service.TakeOver(r.Context(), q, tc.TenantID, conversationID, tc.ActorID, req.Reason, r.Header.Get("X-Request-ID"))
	switch {
	case err == nil:
	case errors.Is(err, ErrReasonNeeded):
		http.Error(w, "a reason is required", http.StatusBadRequest)
		return
	case errors.Is(err, ErrNotFound):
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	case errors.Is(err, ErrNotExternal):
		http.Error(w, "conversation is not held by an external application", http.StatusConflict)
		return
	case errors.Is(err, ErrClosed):
		http.Error(w, "conversation is finalized", http.StatusConflict)
		return
	default:
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if h.audit != nil {
		if ev, aerr := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, auditdomain.ActionConversationTakenOver, auditdomain.ResourceConversation, conversationID, auditdomain.OutcomeSuccess, uuid.Nil); aerr == nil {
			ev.SetMetadata("control_version", st.Version) // the reason is in conversation_control_events; audit carries ids and counters only
			_ = h.audit.Store(r.Context(), ev)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(takeoverResponse{ConversationID: conversationID, Kind: string(*st.Kind), Version: st.Version})
}
