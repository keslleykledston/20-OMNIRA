package adapters

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/ports"
)

// CONTEXT.2C's HTTP surface: GET .../context-view and POST .../context-analysis/refresh. Real Postgres, FORCE RLS,
// runtime role, through the handler directly (env.call), same harness as topics_http_test.go.

func (e *env) contextViewHandler() *TopicHandler {
	repo := NewPostgresTopicRepository(e.app)
	h := NewTopicHandler(e.app, application.NewTopicService(repo), repo)
	analysis := NewPostgresContextAnalysisRepository(e.app)
	windows := NewPostgresContextWindowStore(e.app)
	h.WithContextView(application.NewContextViewService(analysis), application.NewContextRefreshService(windows))
	return h
}

type contextViewResponse struct {
	AnalysisState string `json:"analysis_state"`
	Stale         bool   `json:"stale"`
	Summary       string `json:"summary"`
	Coverage      struct {
		AnalyzedMessageCount     int `json:"analyzed_message_count"`
		TotalInboundMessageCount int `json:"total_inbound_message_count"`
	} `json:"coverage"`
	Topics []struct {
		TopicID uuid.UUID `json:"topic_id"`
		Title   string    `json:"title"`
		Facts   []struct {
			Kind   string `json:"kind"`
			Status string `json:"status"`
		} `json:"facts"`
	} `json:"topics"`
	PendingActions []struct {
		Kind string `json:"kind"`
	} `json:"pending_actions"`
	Suggestions []struct {
		Kind string `json:"kind"`
	} `json:"suggestions"`
	EvidenceRefs                  []uuid.UUID `json:"evidence_refs"`
	NeedsCustomerAccountSelection *bool       `json:"needs_customer_account_selection,omitempty"`
}

func TestContextViewHTTPNoneStateThenOkAfterAnAnalysisExists(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	h := e.contextViewHandler()
	agent := e.member(a.id, "tenant_agent")
	cp := p("conversation_id", a.conversation.String())

	rec := e.call(a.id, agent, http.MethodGet, "", cp, h.GetContextView)
	if rec.Code != http.StatusOK {
		t.Fatalf("none state = %d %s", rec.Code, rec.Body.String())
	}
	var view contextViewResponse
	decodeBody(t, rec, &view)
	if view.AnalysisState != "none" || view.Stale {
		t.Fatalf("fresh conversation with no messages must read as none/not-stale: %+v", view)
	}

	msg := e.message(a.id, a.conversation, "quando chega minha entrega?")
	topicID := e.topic(a.id, "Entrega")
	win := e.contextWindow(a.id, a.conversation)
	analysisRepo := NewPostgresContextAnalysisRepository(e.app)
	if _, err := sys(e, a.id, func(ctx context.Context) (uuid.UUID, error) {
		return analysisRepo.Save(ctx, ports.AnalysisRecord{
			TenantID: a.id, ConversationID: a.conversation, WindowID: win, Status: "ok", SummaryText: "cliente perguntou sobre a entrega",
			AnalyzedMessageIDs: []uuid.UUID{msg},
			Facts: []ports.ResolvedFact{
				{TopicThreadID: topicID, Kind: "need", Text: "saber previsão de entrega", EvidenceMessageIDs: []uuid.UUID{msg}},
				{TopicThreadID: topicID, Kind: "suggested_action", Text: "oferecer rastreio", EvidenceMessageIDs: []uuid.UUID{msg}},
			},
		})
	}); err != nil {
		t.Fatal(err)
	}

	rec = e.call(a.id, agent, http.MethodGet, "", cp, h.GetContextView)
	if rec.Code != http.StatusOK {
		t.Fatalf("ok state = %d %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &view)
	// Stale is not asserted here: e.contextWindow() plants its own inbound message to get a valid window_id, which
	// this analysis never claims to have analyzed — staleness math itself is covered by the application package's
	// own unit tests.
	if view.AnalysisState != "ok" || view.Summary == "" {
		t.Fatalf("after analysis: %+v", view)
	}
	if len(view.Topics) != 1 || view.Topics[0].TopicID != topicID || len(view.Topics[0].Facts) != 2 {
		t.Fatalf("topics = %+v", view.Topics)
	}
	if len(view.PendingActions) != 1 || view.PendingActions[0].Kind != "need" {
		t.Fatalf("pending actions = %+v", view.PendingActions)
	}
	if len(view.Suggestions) != 1 || view.Suggestions[0].Kind != "suggested_action" {
		t.Fatalf("suggestions = %+v", view.Suggestions)
	}
	if len(view.EvidenceRefs) != 1 || view.EvidenceRefs[0] != msg {
		t.Fatalf("evidence refs = %+v", view.EvidenceRefs)
	}
	if view.NeedsCustomerAccountSelection != nil {
		t.Fatalf("an agent without account.read must not even see the field, got %v", *view.NeedsCustomerAccountSelection)
	}
}

func TestContextViewHTTPRequiresTopicReadAndRefusesAnUnknownOrForeignConversation(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	h := e.contextViewHandler()
	viewer := e.readOnlyMember(a.id) // tenant.read only, no topic.read
	agent := e.member(a.id, "tenant_agent")

	if rec := e.call(a.id, viewer, http.MethodGet, "", p("conversation_id", a.conversation.String()), h.GetContextView); rec.Code != http.StatusForbidden {
		t.Fatalf("without topic.read = %d, want 403", rec.Code)
	}
	if rec := e.call(a.id, agent, http.MethodGet, "", p("conversation_id", uuid.NewString()), h.GetContextView); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown conversation = %d, want 404", rec.Code)
	}
	if rec := e.call(a.id, agent, http.MethodGet, "", p("conversation_id", b.conversation.String()), h.GetContextView); rec.Code != http.StatusNotFound {
		t.Fatalf("another tenant's conversation = %d, want 404 (never distinguishable from unknown)", rec.Code)
	}
}

func TestContextRefreshHTTPForcesTheOpenWindowIsIdempotentAndIsGatedLikeEveryOtherMutation(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	h := e.contextViewHandler()
	attendant := e.member(a.id, "tenant_agent")
	stranger := e.member(a.id, "tenant_agent")
	conv := e.conversationFor(a.id, &attendant)
	cp := p("conversation_id", conv.String())

	// an agent who does not attend the conversation (and lacks conversation.manage) cannot trigger it
	if rec := e.call(a.id, stranger, http.MethodPost, "", cp, h.RefreshContextAnalysis); rec.Code != http.StatusForbidden {
		t.Fatalf("non-attendant refresh = %d, want 403", rec.Code)
	}

	msg := e.message(a.id, conv, "mensagem pendente de análise")
	e.exec(`INSERT INTO context_analysis_windows (id, tenant_id, conversation_id, generation, state, first_message_at, first_message_id, last_message_at, last_message_id, message_count, due_at)
	        SELECT $1, $2, $3, COALESCE((SELECT max(generation) FROM context_analysis_windows WHERE tenant_id=$2 AND conversation_id=$3), 0) + 1,
	               'open', now(), $4, now(), $4, 1, now() + interval '1 minute'`, uuid.New(), a.id, conv, msg)
	if e.count(`SELECT count(*) FROM context_analysis_windows WHERE tenant_id=$1 AND conversation_id=$2 AND state='open'`, a.id, conv) != 1 {
		t.Fatal("setup: expected one open window")
	}

	rec := e.call(a.id, attendant, http.MethodPost, "", cp, h.RefreshContextAnalysis)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("refresh = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Forced bool `json:"forced"`
	}
	decodeBody(t, rec, &body)
	if !body.Forced {
		t.Fatalf("forced = %v, want true (there was an open window)", body.Forced)
	}
	if e.count(`SELECT count(*) FROM context_analysis_windows WHERE tenant_id=$1 AND conversation_id=$2 AND state='due'`, a.id, conv) != 1 {
		t.Fatal("refresh must force the open window to due")
	}

	// immediately again: throttled, not another forced attempt (idempotent/rate limited, CONTEXT.2C)
	rec = e.call(a.id, attendant, http.MethodPost, "", cp, h.RefreshContextAnalysis)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("immediate second refresh = %d, want 429", rec.Code)
	}
}
