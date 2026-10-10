package adapters_test

// ADR-0040 phase 07: a Hub agent asks for the AI DRAFT of the closing (summary and follow-ups) of a conversation THEY HOLD, through the instance's own attendance route.
// On a real PostgreSQL with the application's role, the REAL attendance handler and service and the REAL reader of the closing messages (the model is a fake that
// reports what it was given). What must hold:
//   - the key ai.assist AND conversation.read are needed (the text of the conversation goes to the platform's AI provider); without either the provider is never reached;
//   - only the HOLDER gets a draft (no supervisor path for a delegate); a closed conversation has nothing to suggest;
//   - the model only ever sees the text of the conversation the person may see: outside the contract's queues and another instance's conversation never reach it;
//   - the draft is never stored; a member keeps the original route; a server that does not wire the check leaves the route closed to a delegate.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	attendanceadapters "github.com/omnira/omnira/internal/attendance/adapters"
	attendanceapplication "github.com/omnira/omnira/internal/attendance/application"
	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	"github.com/omnira/omnira/internal/hub/provisioning"
	intelligenceadapters "github.com/omnira/omnira/internal/intelligence/adapters"
	intelligenceports "github.com/omnira/omnira/internal/intelligence/ports"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// fakeDrafter stands for the model: it reads the conversation through the real reader (so Postgres decides what it sees) and answers with what it read.
type fakeDrafter struct {
	w       *world
	reader  *intelligenceadapters.PostgresClosingReader
	calls   atomic.Int32
	sources atomic.Value // the access source of the last call
	// betweenCheckAndRead runs after the service's check and before the text is read: the window in which the conversation can change hands
	betweenCheckAndRead func()
	provider            atomic.Int32 // how many times text was actually handed to the "provider"
}

func (f *fakeDrafter) SuggestClosing(ctx context.Context, conversationID uuid.UUID) (attendancedomain.ClosingSuggestion, error) {
	return f.draft(ctx, conversationID, nil)
}

// SuggestClosingHeldBy is the holder-bound variant the Hub agent's route requires of the suggester.
func (f *fakeDrafter) SuggestClosingHeldBy(ctx context.Context, conversationID, holder uuid.UUID) (attendancedomain.ClosingSuggestion, error) {
	return f.draft(ctx, conversationID, &holder)
}

func (f *fakeDrafter) draft(ctx context.Context, conversationID uuid.UUID, holder *uuid.UUID) (attendancedomain.ClosingSuggestion, error) {
	f.calls.Add(1)
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil {
		return attendancedomain.ClosingSuggestion{}, err
	}
	f.sources.Store(string(tc.Source))
	if f.betweenCheckAndRead != nil {
		f.betweenCheckAndRead()
	}
	var msgs []intelligenceports.ClosingMessage
	if holder != nil {
		msgs, err = f.reader.RecentMessagesHeldBy(ctx, tc.TenantID, conversationID, *holder, 20)
	} else {
		msgs, err = f.reader.RecentMessages(ctx, tc.TenantID, conversationID, 20)
	}
	if err != nil {
		return attendancedomain.ClosingSuggestion{}, err
	}
	if len(msgs) == 0 {
		return attendancedomain.ClosingSuggestion{}, attendancedomain.ErrNothingToSuggest
	}
	f.provider.Add(1)
	var seen []string
	for _, m := range msgs {
		seen = append(seen, m.Text)
	}
	return attendancedomain.ClosingSuggestion{Summary: "RASCUNHO: " + strings.Join(seen, " / "), Model: "fake", BasedOnMessages: len(msgs)}, nil
}

func TestDelegatedClosingDraftThroughTheRealAttendanceRoute(t *testing.T) {
	w := newWorld(t)
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	drafter := &fakeDrafter{w: w, reader: intelligenceadapters.NewPostgresClosingReader(w.app)}
	build := func(wired bool, perMinute int) http.Handler {
		svc := attendanceapplication.NewService(attendanceadapters.NewPostgresRepository(w.app), attendanceadapters.NewAuthorizer(w.app),
			attendanceadapters.NewAuditor(auditadapters.NewPostgresAuditEventRepository(w.app))).WithSuggester(drafter)
		handler := attendanceadapters.NewHandler(svc)
		if wired {
			handler.WithDelegatedSuggest(func(ctx context.Context, tenantID, userID uuid.UUID) (bool, error) {
				return tenancyadapters.ActorHasPermission(ctx, platformdb.QuerierFromContext(ctx, w.app), tenantID, userID, "conversation.read")
			}, perMinute)
		}
		authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(w.app), tenancyadapters.NewPostgresTenantRepository(w.app))
		session := tenancyadapters.AuthorizationMiddleware(w.app, authz)
		who := func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				if u, err := uuid.Parse(r.Header.Get("X-Test-User")); err == nil {
					r = r.WithContext(authn.WithPrincipal(r.Context(), &authn.Principal{UserID: u}))
				}
				next.ServeHTTP(rw, r)
			})
		}
		mux := http.NewServeMux()
		handler.Routes(mux, func(fn http.HandlerFunc) http.Handler { return who(session(fn)) })
		return mux
	}
	mux := build(true, 1000)
	suggest := func(mux http.Handler, user uuid.UUID, tenantKey, convKey, acting string) (int, string) {
		req := httptest.NewRequest("POST", "/api/v1/tenants/"+w.tenant[tenantKey].String()+"/inbox/conversations/"+w.conv[convKey].String()+"/finalize/suggest", strings.NewReader("{}"))
		req.Header.Set("X-Test-User", user.String())
		req.Header.Set("Content-Type", "application/json")
		if acting != "" {
			req.Header.Set("X-Omnira-Acting-As", acting)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	hub := "hub:" + w.hub.String()
	withAI := append(append([]string(nil), provisioning.ServingPresets["completo"]...), "ai.assist")
	w.ceiling("A", withAI...)
	w.ceiling("B", withAI...)
	agent := w.colleague("drafter", "A", withAI...)
	noAI := w.colleague("no-ai", "A", provisioning.ServingPresets["completo_fin"]...)
	noRead := w.colleague("no-read", "A", "ai.assist", "contact.read") // may send text to the provider, but cannot read the conversation
	other := w.colleague("other", "A", withAI...)
	member := w.user("member")
	w.directMember(member, "A")
	text := func(convKey, body string) {
		w.exec(`INSERT INTO messages (tenant_id, conversation_id, direction, message_type, body) VALUES ($1, $2, 'inbound', 'text', $3)`, w.tenant[tenantKey(convKey)], w.conv[convKey], body)
	}
	text("A", "Minha conexão caiu ontem à noite")
	text("A2", "Texto da fila que o contrato não cobre")
	text("B", "Texto de outra empresa")
	hold := func(convKey string, user *uuid.UUID) {
		w.exec(`UPDATE conversations SET assigned_to_user_id = $2, status = 'open', closed_at = NULL WHERE id = $1`, w.conv[convKey], user)
	}
	calls := func() int32 { return drafter.calls.Load() }

	t.Run("the holder with ai.assist and conversation.read gets the draft of THIS conversation only", func(t *testing.T) {
		hold("A", &agent)
		before := calls()
		code, body := suggest(mux, agent, "A", "A", hub)
		if code != 200 || !strings.Contains(body, "Minha conexão caiu") || !strings.Contains(body, `"summary_truth":"ai_inferred"`) {
			t.Fatalf("draft: %d %s", code, body)
		}
		if strings.Contains(body, "fila que o contrato não cobre") || strings.Contains(body, "outra empresa") {
			t.Errorf("text of another conversation reached the model: %s", body)
		}
		if calls() != before+1 || drafter.sources.Load() != string(tenancydomain.AccessSourceHubServe) {
			t.Errorf("the model was reached %d times (source %v), want once in the delegated context", calls()-before, drafter.sources.Load())
		}
		if n := w.count(`SELECT count(*) FROM conversation_closures WHERE conversation_id = $1`, w.conv["A"]); n != 0 {
			t.Error("a draft stored a closure")
		}
		if st := w.ownerOne(`SELECT status FROM conversations WHERE id = $1`, w.conv["A"]); st != "open" {
			t.Errorf("a draft changed the conversation: %s", st)
		}
	})

	t.Run("without ai.assist, or with it but without conversation.read, the model is never reached", func(t *testing.T) {
		hold("A", &noAI)
		before := calls()
		if code, _ := suggest(mux, noAI, "A", "A", hub); code != 403 {
			t.Errorf("without ai.assist: %d, want 403", code)
		}
		hold("A", &noRead)
		if code, _ := suggest(mux, noRead, "A", "A", hub); code != 403 {
			t.Errorf("without conversation.read: %d, want 403", code)
		}
		if calls() != before {
			t.Error("the provider was reached without the keys")
		}
	})

	t.Run("only the holder: not a colleague, not an unassigned conversation, not a closed one", func(t *testing.T) {
		before := calls()
		hold("A", &other)
		if code, _ := suggest(mux, agent, "A", "A", hub); code != 403 {
			t.Errorf("a draft for a conversation of a colleague: %d, want 403", code)
		}
		hold("A", nil)
		if code, _ := suggest(mux, agent, "A", "A", hub); code != 403 {
			t.Errorf("a draft for an unassigned conversation: %d, want 403 (no supervisor path for a delegate)", code)
		}
		if calls() != before {
			t.Error("the provider was reached for a conversation the person does not hold")
		}
		hold("A", &agent)
		w.exec(`UPDATE conversations SET status = 'closed' WHERE id = $1`, w.conv["A"])
		if code, body := suggest(mux, agent, "A", "A", hub); code != 422 || !strings.Contains(body, "nothing_to_suggest") {
			t.Errorf("a draft for a closed conversation: %d %s, want 422 nothing_to_suggest", code, body)
		}
		hold("A", &agent)
		// a conversation between colleagues (internal) is not an attendance of a contact
		hold("A0", &agent)
		contactA := w.contactOf("A")
		w.exec(`UPDATE conversations SET conversation_kind = 'internal', internal_user_id = $2, contact_id = NULL WHERE id = $1`, w.conv["A0"], member)
		t.Cleanup(func() {
			w.exec(`UPDATE conversations SET conversation_kind = 'unclassified', internal_user_id = NULL, contact_id = $2 WHERE id = $1`, w.conv["A0"], contactA)
		})
		text("A0", "conversa interna")
		before = calls()
		if code, body := suggest(mux, agent, "A", "A0", hub); code != 422 || !strings.Contains(body, "not_a_contact_conversation") {
			t.Errorf("a draft for an internal conversation: %d %s, want 422 not_a_contact_conversation", code, body)
		}
		if calls() != before {
			t.Error("the provider was reached for an internal conversation")
		}
	})

	t.Run("a conversation that changes hands between the check and the read never reaches the provider", func(t *testing.T) {
		hold("A", &agent)
		before, provided := calls(), drafter.provider.Load()
		drafter.betweenCheckAndRead = func() { hold("A", &other) } // a supervisor reassigns it right after the service's check
		t.Cleanup(func() { drafter.betweenCheckAndRead = nil })
		if code, body := suggest(mux, agent, "A", "A", hub); code != 422 || strings.Contains(body, "Minha conexão") {
			t.Errorf("a draft after the conversation changed hands: %d %s, want 422 and no text", code, body)
		}
		if drafter.provider.Load() != provided {
			t.Error("the text of a conversation the person no longer held was handed to the provider")
		}
		if calls() != before+1 {
			t.Errorf("the suggester was reached %d times, want once", calls()-before)
		}
		drafter.betweenCheckAndRead = func() { w.exec(`UPDATE conversations SET status = 'closed' WHERE id = $1`, w.conv["A"]) }
		hold("A", &agent)
		drafter.betweenCheckAndRead = func() { w.exec(`UPDATE conversations SET status = 'closed' WHERE id = $1`, w.conv["A"]) }
		if code, _ := suggest(mux, agent, "A", "A", hub); code != 422 {
			t.Errorf("a draft after the conversation was closed: %d, want 422", code)
		}
		if drafter.provider.Load() != provided {
			t.Error("the text of a closed conversation was handed to the provider")
		}
		drafter.betweenCheckAndRead = nil
		hold("A", &agent)
	})

	t.Run("each person has a limit of drafts, because every draft is a paid call", func(t *testing.T) {
		hold("A", &agent)
		limited := build(true, 2)
		before := calls()
		for i, want := range []int{200, 200, 429} {
			if code, _ := suggest(limited, agent, "A", "A", hub); code != want {
				t.Errorf("draft %d: %d, want %d", i+1, code, want)
			}
		}
		if calls() != before+2 {
			t.Errorf("the provider was reached %d times, want 2", calls()-before)
		}
		// the limit is per person: the colleague is not limited by it
		hold("A", &other)
		if code, _ := suggest(limited, other, "A", "A", hub); code != 200 {
			t.Errorf("another person: %d, want 200", code)
		}
		hold("A", &agent)
	})

	t.Run("the walls of the instance: outside the contract's queues, another instance, a revoked grant", func(t *testing.T) {
		before := calls()
		hold("A2", &agent)
		w.queueScope("A", w.queue1["A"])
		t.Cleanup(func() {
			w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}'::jsonb WHERE id = $1`, w.contract["A"])
		})
		if code, _ := suggest(mux, agent, "A", "A2", hub); code != 404 {
			t.Errorf("a conversation outside the contract's queues: %d, want 404", code)
		}
		w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}'::jsonb WHERE id = $1`, w.contract["A"])
		req := httptest.NewRequest("POST", "/api/v1/tenants/"+w.tenant["A"].String()+"/inbox/conversations/"+w.conv["B"].String()+"/finalize/suggest", strings.NewReader("{}"))
		req.Header.Set("X-Test-User", agent.String())
		req.Header.Set("X-Omnira-Acting-As", hub)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Errorf("another instance's conversation under this URL: %d, want 404", rec.Code)
		}
		g := w.ownerOne(`SELECT id::text FROM effective_access_grants WHERE user_id = $1 AND tenant_id = $2`, agent, w.tenant["A"])
		w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, g)
		if code, _ := suggest(mux, agent, "A", "A", hub); code != 404 {
			t.Errorf("a revoked grant: %d, want 404", code)
		}
		w.exec(`UPDATE effective_access_grants SET status = 'active' WHERE id = $1`, g)
		if calls() != before {
			t.Error("the provider was reached through a closed door")
		}
	})

	t.Run("a member keeps the original route, and a server that does not wire the check leaves the route closed to a delegate", func(t *testing.T) {
		hold("A", &member)
		before := calls()
		if code, body := suggest(mux, member, "A", "A", ""); code != 200 || !strings.Contains(body, "Minha conexão caiu") {
			t.Errorf("member draft: %d %s", code, body)
		}
		if drafter.sources.Load() == string(tenancydomain.AccessSourceHubServe) {
			t.Error("the member's request ran in the delegated context")
		}
		closed := build(false, 0)
		hold("A", &agent)
		if code, _ := suggest(closed, agent, "A", "A", hub); code != 404 {
			t.Errorf("a delegate on a server that does not wire it: %d, want 404 (not delegable)", code)
		}
		if calls() != before+1 {
			t.Error("the provider was reached by a delegate on an unwired route")
		}
	})
}

// tenantKey maps the fixture's conversation keys to the instance they belong to.
func tenantKey(convKey string) string {
	if strings.HasPrefix(convKey, "B") {
		return "B"
	}
	return "A"
}
