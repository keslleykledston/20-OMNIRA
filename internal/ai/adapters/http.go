package adapters

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/ai/application"
	"github.com/omnira/omnira/internal/ai/ports"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/ratelimit"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// SummaryHandler serves POST .../conversations/{conversation_id}/ai/summary
// (PRODUCT.7C1). It never imports OpenAI-specific types — only
// ports.TextGenerator — and never persists a generated summary.
type SummaryHandler struct {
	pool      *pgxpool.Pool
	service   *application.SummarizeService // nil when AI is disabled/unconfigured
	auditRepo auditports.AuditEventRepository
	limiter   *ratelimit.Limiter
}

// NewSummaryHandler. generator may be nil — that IS the "AI disabled or
// misconfigured" state (PRODUCT.7C0 §4/§10: the optional AI subsystem fails
// closed by never being constructed; the handler always exists and always
// reports a truthful unavailable state, so the endpoint's mere presence
// never depends on configuration).
func NewSummaryHandler(pool *pgxpool.Pool, generator ports.TextGenerator, auditRepo auditports.AuditEventRepository, maxOutputTokens int) *SummaryHandler {
	h := &SummaryHandler{pool: pool, auditRepo: auditRepo}
	if generator != nil {
		h.service = application.NewSummarizeService(generator, maxOutputTokens)
	}
	// Dedicated limiter, deliberately separate from the server's global
	// per-tenant/per-user API rate limiter (internal/platform/httpserver):
	// this budget exists specifically to bound paid external-provider
	// traffic, not general API traffic, and must never be loosened by a
	// change to the unrelated global quotas (PRODUCT.7C1 §13, pilot target:
	// 5/min/user).
	h.limiter = ratelimit.NewLimiter()
	h.limiter.SetQuota(ratelimit.QuotaTypeUser, 5, time.Minute)
	return h
}

func requestTenantContext(r *http.Request) (*tenancydomain.TenantContext, error) {
	return tenancydomain.FromContext(r.Context())
}

func (h *SummaryHandler) Summarize(w http.ResponseWriter, r *http.Request) {
	tc, err := requestTenantContext(r)
	if err != nil || tc == nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	conversationID, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return
	}

	// Fail closed unconditionally when AI is disabled/unconfigured — no
	// tenant lookup, no rate-limit check, no audit write, zero provider
	// calls, before anything else happens.
	if h.service == nil {
		http.Error(w, "ai summary is not available", http.StatusServiceUnavailable)
		return
	}

	// Conversation must belong to this tenant. Tenant URL alone is never
	// authorization — this reuses the exact same tenant-scoped RLS query
	// shape internal/inbox/adapters.GetConversation already uses; a foreign
	// tenant's conversation is indistinguishable from an unknown one (404).
	var exists bool
	err = platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(),
		`SELECT true FROM conversations WHERE tenant_id=$1 AND id=$2`, tc.TenantID, conversationID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to verify conversation", http.StatusInternalServerError)
		return
	}

	allowed, _, resetTime := h.limiter.Allow(r.Context(), ratelimit.QuotaTypeUser, "ai-summary:"+tc.ActorID.String())
	if !allowed {
		w.Header().Set("Retry-After", resetTime.UTC().Format(time.RFC1123))
		http.Error(w, "too many summary requests, try again shortly", http.StatusTooManyRequests)
		return
	}

	messages, err := h.loadRecentMessages(r, tc.TenantID, conversationID)
	if err != nil {
		http.Error(w, "failed to load conversation messages", http.StatusInternalServerError)
		return
	}

	correlationID := uuid.New()

	// PRE-CALL audit write (PRODUCT.7C1 §11). If this fails, the provider is
	// NEVER called — an unauditable external transfer of conversation
	// content is not an acceptable failure mode, even a rare one.
	inputCount, inputChars := previewStats(messages)
	if err := h.writeAudit(r, tc, conversationID, correlationID, auditdomain.OutcomeSuccess, map[string]interface{}{
		"phase":               "requested",
		"provider":            "openai",
		"input_message_count": inputCount,
		"input_char_count":    inputChars,
	}); err != nil {
		http.Error(w, "failed to record required audit event; summary not requested", http.StatusInternalServerError)
		return
	}

	result, err := h.service.Summarize(r.Context(), messages)

	outcome := auditdomain.OutcomeSuccess
	category := "success"
	status := http.StatusOK
	var responseErr string
	switch {
	case err == nil:
		// success
	case errors.Is(err, application.ErrEmptyTranscript):
		outcome, category, status, responseErr = auditdomain.OutcomeFailure, "empty_transcript", http.StatusUnprocessableEntity, "no summarizable content in this conversation"
	case errors.Is(err, ErrProviderUnauthorized):
		outcome, category, status, responseErr = auditdomain.OutcomeFailure, "provider_error", http.StatusBadGateway, "ai summary is not available"
	case errors.Is(err, ErrProviderRateLimited):
		outcome, category, status, responseErr = auditdomain.OutcomeFailure, "rate_limited", http.StatusTooManyRequests, "ai provider is rate-limited, try again shortly"
	case errors.Is(err, ErrProviderTimeout):
		outcome, category, status, responseErr = auditdomain.OutcomeFailure, "timeout", http.StatusGatewayTimeout, "ai summary timed out, try again"
	case errors.Is(err, ErrProviderUnavailable):
		outcome, category, status, responseErr = auditdomain.OutcomeFailure, "provider_error", http.StatusBadGateway, "ai provider unavailable, try again shortly"
	default:
		outcome, category, status, responseErr = auditdomain.OutcomeFailure, "provider_error", http.StatusBadGateway, "ai summary failed"
	}

	// POST-CALL audit write. Best-effort: a failure here must not turn a
	// real result into an error, retry, or a second provider call — the
	// PRE-CALL write above already proves the external transfer happened,
	// so the response already reflects ground truth independent of this
	// write's success. It must still be operationally visible, so a failure
	// here is logged (IDs/category only — never message/summary content).
	if auditErr := h.writeAudit(r, tc, conversationID, correlationID, outcome, map[string]interface{}{
		"phase":           "completed",
		"provider":        "openai",
		"result_category": category,
	}); auditErr != nil {
		log.Printf("ai summary: post-call audit write failed (tenant=%s conversation=%s correlation=%s outcome=%s category=%s): %v",
			tc.TenantID, conversationID, correlationID, outcome, category, auditErr)
	}

	if responseErr != "" {
		http.Error(w, responseErr, status)
		return
	}
	writeJSONSummary(w, result.Summary)
}

func previewStats(messages []application.Message) (int, int) {
	_, stats := application.BuildTranscript(messages)
	return stats.InputMessageCount, stats.InputCharCount
}

func (h *SummaryHandler) writeAudit(r *http.Request, tc *tenancydomain.TenantContext, conversationID, correlationID uuid.UUID, outcome auditdomain.AuditOutcome, metadata map[string]interface{}) error {
	event, err := auditdomain.NewAuditEvent(
		tc.TenantID,
		tc.ActorID,
		auditdomain.ActionAIConversationSummarize,
		auditdomain.ResourceConversation,
		conversationID,
		outcome,
		correlationID,
	)
	if err != nil {
		return err
	}
	event.Metadata = metadata
	return h.auditRepo.Store(r.Context(), event)
}

// loadRecentMessages fetches up to application.MaxWindowMessages most recent
// messages of the conversation, tenant-scoped via the request's already-set
// RLS session (platformdb.QuerierFromContext) — never a second, unscoped
// pool query. Maps directly into the provider-neutral application.Message
// shape: body text and media presence only, nothing else (PRODUCT.7C0 §5:
// no contact name, no phone/email, no external IDs, no queue name).
func (h *SummaryHandler) loadRecentMessages(r *http.Request, tenantID, conversationID uuid.UUID) ([]application.Message, error) {
	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), `
		SELECT id, direction, message_type, body, status, created_at
		FROM messages
		WHERE tenant_id=$1 AND conversation_id=$2
		ORDER BY created_at DESC, id DESC
		LIMIT $3`, tenantID, conversationID, application.MaxWindowMessages)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]application.Message, 0, application.MaxWindowMessages)
	for rows.Next() {
		var id uuid.UUID
		var direction, messageType, body, status string
		var createdAt time.Time
		if err := rows.Scan(&id, &direction, &messageType, &body, &status, &createdAt); err != nil {
			return nil, err
		}
		out = append(out, application.Message{
			ID:        id.String(),
			Direction: direction,
			Status:    status,
			Body:      body,
			HasMedia:  messageType != "text",
			CreatedAt: createdAt,
		})
	}
	return out, rows.Err()
}

func writeJSONSummary(w http.ResponseWriter, summary string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"summary": strings.TrimSpace(summary)})
}
