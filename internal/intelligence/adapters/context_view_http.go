package adapters

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// CONTEXT.2C: a server-only aggregated read model over CONTEXT.2B's analyses, and the single "manual" write that
// records intent to re-analyze now. Reading goes through tenancyadapters.ActorHasPermission (not h.has, which only
// ever sees membership rows) so a Hub-delegated operator's grant is honoured the same way ADR-0040 already honours
// it for the plain inbox endpoints — the permission key is checked live, in the request's own session, for both a
// member and a delegated actor; GetContextView's route is marked Delegable("conversation.read", ...) in
// RegisterIntelligenceHandlers for exactly that reason.

type contextViewFactDTO struct {
	ID                 uuid.UUID   `json:"id"`
	Kind               string      `json:"kind"`
	Text               string      `json:"text"`
	TruthLevel         string      `json:"truth_level"`
	Status             string      `json:"status"`
	EvidenceMessageIDs []uuid.UUID `json:"evidence_message_ids"`
	CreatedAt          string      `json:"created_at"`
}

func toContextViewFactDTO(f application.ContextViewFact) contextViewFactDTO {
	ids := f.EvidenceMessageIDs
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return contextViewFactDTO{ID: f.ID, Kind: f.Kind, Text: f.Text, TruthLevel: f.TruthLevel, Status: f.Status, EvidenceMessageIDs: ids, CreatedAt: ts(f.CreatedAt)}
}

type contextViewTopicDTO struct {
	TopicID uuid.UUID            `json:"topic_id"`
	Title   string               `json:"title"`
	Status  string               `json:"status"`
	Facts   []contextViewFactDTO `json:"facts"`
}

type contextViewCoverageDTO struct {
	AnalyzedMessageCount     int     `json:"analyzed_message_count"`
	TotalInboundMessageCount int     `json:"total_inbound_message_count"`
	LastAnalyzedMessageAt    *string `json:"last_analyzed_message_at,omitempty"`
}

type contextViewDTO struct {
	AnalysisState                 string                 `json:"analysis_state"` // none | ok | empty | failed
	UpdatedAt                     *string                `json:"updated_at,omitempty"`
	Summary                       string                 `json:"summary"`
	Stale                         bool                   `json:"stale"`
	Coverage                      contextViewCoverageDTO `json:"coverage"`
	Topics                        []contextViewTopicDTO  `json:"topics"`
	PendingActions                []contextViewFactDTO   `json:"pending_actions"`
	Suggestions                   []contextViewFactDTO   `json:"suggestions"`
	EvidenceRefs                  []uuid.UUID            `json:"evidence_refs"`
	NeedsCustomerAccountSelection *bool                  `json:"needs_customer_account_selection,omitempty"`
}

func toContextViewDTO(v application.ContextView) contextViewDTO {
	dto := contextViewDTO{
		AnalysisState: v.AnalysisState, UpdatedAt: tsp(v.UpdatedAt), Summary: v.Summary, Stale: v.Stale,
		Coverage: contextViewCoverageDTO{AnalyzedMessageCount: v.Coverage.AnalyzedMessageCount, TotalInboundMessageCount: v.Coverage.TotalInboundMessageCount,
			LastAnalyzedMessageAt: tsp(v.Coverage.LastAnalyzedMessageAt)},
		Topics: make([]contextViewTopicDTO, 0, len(v.Topics)), PendingActions: make([]contextViewFactDTO, 0, len(v.PendingActions)),
		Suggestions: make([]contextViewFactDTO, 0, len(v.Suggestions)), EvidenceRefs: v.EvidenceMessageIDs,
	}
	if dto.EvidenceRefs == nil {
		dto.EvidenceRefs = []uuid.UUID{}
	}
	for _, t := range v.Topics {
		facts := make([]contextViewFactDTO, 0, len(t.Facts))
		for _, f := range t.Facts {
			facts = append(facts, toContextViewFactDTO(f))
		}
		dto.Topics = append(dto.Topics, contextViewTopicDTO{TopicID: t.TopicThreadID, Title: t.Title, Status: t.Status, Facts: facts})
	}
	for _, f := range v.PendingActions {
		dto.PendingActions = append(dto.PendingActions, toContextViewFactDTO(f))
	}
	for _, f := range v.Suggestions {
		dto.Suggestions = append(dto.Suggestions, toContextViewFactDTO(f))
	}
	return dto
}

// requirePermission checks a permission key the way a Hub-delegated session also needs it checked: live, via the
// actor_has_permission() database function, never the raw membership join h.has() uses (which a delegated actor,
// having no membership row, could never satisfy).
func requirePermission(ctx context.Context, q platformdb.Querier, tenantID, actorID uuid.UUID, permission string) (bool, error) {
	return tenancyadapters.ActorHasPermission(ctx, q, tenantID, actorID, permission)
}

func (h *TopicHandler) needsCustomerAccountSelection(ctx context.Context, tenantID uuid.UUID, topics []application.ContextViewTopic) (bool, error) {
	for _, t := range topics {
		ac, err := h.accountContext(ctx, tenantID, t.TopicThreadID)
		if err != nil {
			return false, err
		}
		if ac.Status == "needs_choice" {
			return true, nil
		}
	}
	return false, nil
}

// GetContextView: GET /tenants/{tenant_id}/inbox/conversations/{conversation_id}/context-view (CONTEXT.2C)
// Needs conversation.read AND topic.read. needs_customer_account_selection is present only for an actor who also
// holds account.read — omitted (never a visible-but-disabled field) for anyone else, the same redaction-not-hiding
// rule ADR-0040 asks for.
func (h *TopicHandler) GetContextView(w http.ResponseWriter, r *http.Request) {
	if h.contextView == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		fail(w, errors.New("tenant context not found"))
		return
	}
	convID, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	// conversation.read itself is never a role_permissions grant for a plain member (ADR-0040 4.1: membership alone
	// already makes the conversation visible) — only the delegated path uses it as a real gate, and the route's
	// Delegable("conversation.read", ...) wrapper already enforced it before this handler ran. topic.read is the one
	// permission genuinely module-specific here, so it is the one re-checked live for both a member and a delegate.
	if held, err := requirePermission(r.Context(), q, tc.TenantID, tc.ActorID, permTopicRead); err != nil {
		fail(w, err)
		return
	} else if !held {
		fail(w, errForbidden)
		return
	}
	info, err := h.repo.ConversationInfo(r.Context(), tc.TenantID, convID)
	if err != nil || !info.Exists {
		fail(w, firstErr(err, domain.ErrReferenceNotFound))
		return
	}
	view, err := h.contextView.Get(r.Context(), tc.TenantID, convID)
	if err != nil {
		fail(w, err)
		return
	}
	dto := toContextViewDTO(view)
	if canAccounts, aerr := requirePermission(r.Context(), q, tc.TenantID, tc.ActorID, permAccountRead); aerr == nil && canAccounts {
		if needs, nerr := h.needsCustomerAccountSelection(r.Context(), tc.TenantID, view.Topics); nerr == nil {
			dto.NeedsCustomerAccountSelection = &needs
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, dto)
}

// RefreshContextAnalysis: POST /tenants/{tenant_id}/inbox/conversations/{conversation_id}/context-analysis/refresh
// (CONTEXT.2C). Same gate as every other conversation-scoped mutation in this handler (topic.manage AND being the
// attendant, or conversation.manage) — this is a membership-path-only action: a Hub-delegated session is not
// wired to trigger analysis, a deliberately smaller slice than read access.
func (h *TopicHandler) RefreshContextAnalysis(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permTopicManage)
	if err != nil {
		fail(w, err)
		return
	}
	if h.refresh == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	convID, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	info, err := h.repo.ConversationInfo(r.Context(), tc.TenantID, convID)
	if err != nil || !info.Exists {
		fail(w, firstErr(err, domain.ErrReferenceNotFound))
		return
	}
	if ok, err := h.canOperateConversation(r.Context(), tc, info.AssignedTo); err != nil || !ok {
		fail(w, firstErr(err, errForbidden))
		return
	}
	forced, err := h.refresh.Refresh(r.Context(), tc.TenantID, convID)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, map[string]any{"forced": forced})
}
