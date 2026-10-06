package adapters_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/attendance/adapters"
	"github.com/omnira/omnira/internal/attendance/application"
	"github.com/omnira/omnira/internal/attendance/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func (s *stack) mux() *http.ServeMux {
	mux := http.NewServeMux()
	adapters.NewHandler(s.svc).Routes(mux, func(fn http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, uerr := uuid.Parse(r.Header.Get("X-Test-User"))
			tenant, terr := uuid.Parse(r.PathValue("tenant_id"))
			if uerr != nil || terr != nil {
				fn(w, r) // no TenantContext: the handler must answer 401
				return
			}
			_ = platformdb.WithTenantSession(r.Context(), s.env.App, user, false, func(sc context.Context) error {
				tc, _ := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
				fn(w, r.WithContext(tenancydomain.WithTenantContext(sc, tc)))
				return nil
			})
		})
	})
	return mux
}

func (s *stack) call(mux *http.ServeMux, user uuid.UUID, method, path, raw string) (int, map[string]any) {
	s.t.Helper()
	req := httptest.NewRequest(method, fmt.Sprintf("/api/v1/tenants/%s%s", s.env.TenantA, path), bytes.NewReader([]byte(raw)))
	if user != uuid.Nil {
		req.Header.Set("X-Test-User", user.String())
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestHTTPFinalizeAndHistory(t *testing.T) {
	s := newStack(t)
	mux := s.mux()
	conv, contact := s.conversation(&s.agent)
	fin := fmt.Sprintf("/inbox/conversations/%s/finalize", conv)
	body := `{"reason":"resolved","summary":"link voltou","follow_ups":[{"kind":"promise","text":"ligar amanhã"}]}`

	if code, _ := s.call(mux, uuid.Nil, "POST", fin, body); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	if code, out := s.call(mux, s.observer, "POST", fin, body); code != http.StatusForbidden || out["error"] != "forbidden" {
		t.Fatalf("no claim/manage: %d %v", code, out)
	}
	if code, _ := s.call(mux, s.agent, "POST", fin, `{"reason":"resolved","tenant_id":"`+uuid.NewString()+`"}`); code != http.StatusBadRequest {
		t.Fatalf("a smuggled tenant_id (unknown field) must be refused: %d", code)
	}
	if code, _ := s.call(mux, s.agent, "POST", fin, `{"reason":"bogus"}`); code != http.StatusBadRequest {
		t.Fatalf("invalid reason: %d", code)
	}
	if code, _ := s.call(mux, s.agent, "POST", fin, `{"reason":"resolved","summary":"Bearer abcdefghijklmnop1234"}`); code != http.StatusBadRequest {
		t.Fatalf("a credential in free text: %d", code)
	}
	if code, _ := s.call(mux, s.agent, "POST", "/inbox/conversations/not-a-uuid/finalize", body); code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", code)
	}
	code, out := s.call(mux, s.agent, "POST", fin, body)
	if code != http.StatusOK || out["changed"] != true {
		t.Fatalf("finalize: %d %v", code, out)
	}
	closure, _ := out["closure"].(map[string]any)
	if closure["reason"] != "resolved" || closure["source"] != "agent" || closure["summary_truth"] != "agent_confirmed" {
		t.Fatalf("closure body: %v", closure)
	}
	if strings.Contains(fmt.Sprint(out), "tenant") {
		t.Fatalf("the response never exposes the tenant: %v", out)
	}
	// idempotent
	code, out = s.call(mux, s.agent, "POST", fin, body)
	if code != http.StatusOK || out["changed"] != false {
		t.Fatalf("second call: %d %v", code, out)
	}
	// history, by conversation and by contact
	code, histBody := s.call(mux, s.observer, "GET", fmt.Sprintf("/inbox/conversations/%s/attendance-context", conv), "")
	if code != http.StatusOK || len(histBody["attendances"].([]any)) != 1 || len(histBody["open_follow_ups"].([]any)) != 1 {
		t.Fatalf("attendance-context: %d %v", code, histBody)
	}
	if code, out := s.call(mux, s.observer, "GET", fmt.Sprintf("/contacts/%s/attendance-history?limit=1", contact), ""); code != http.StatusOK || out["contact_id"] != contact.String() {
		t.Fatalf("attendance-history: %d %v", code, out)
	}
	// resolve
	item := histBody["open_follow_ups"].([]any)[0].(map[string]any)["id"].(string)
	if code, _ := s.call(mux, s.observer, "POST", "/follow-ups/"+item+"/resolve", `{"status":"done"}`); code != http.StatusForbidden {
		t.Fatalf("observer cannot resolve: %d", code)
	}
	if code, out := s.call(mux, s.agent, "POST", "/follow-ups/"+item+"/resolve", `{"status":"done","note":"ok"}`); code != http.StatusOK || out["status"] != "done" {
		t.Fatalf("resolve: %d %v", code, out)
	}
	if code, out := s.call(mux, s.agent, "POST", "/follow-ups/"+item+"/resolve", `{"status":"dropped"}`); code != http.StatusConflict || out["error"] != "already_resolved" {
		t.Fatalf("resolving twice: %d %v", code, out)
	}
}

type fakeSuggester struct {
	out   domain.ClosingSuggestion
	err   error
	calls int
	hold  chan struct{} // when set, the call waits (a slow model)
	seen  chan struct{}
}

func (f *fakeSuggester) SuggestClosing(context.Context, uuid.UUID) (domain.ClosingSuggestion, error) {
	f.calls++
	if f.seen != nil {
		close(f.seen)
	}
	if f.hold != nil {
		<-f.hold
	}
	return f.out, f.err
}

func TestHTTPSuggestClosingIsAuthorizedReadOnlyAndNeverBlocksTheConversation(t *testing.T) {
	s := newStack(t)
	fake := &fakeSuggester{out: domain.ClosingSuggestion{Summary: "Link voltou.", Model: "mini", BasedOnMessages: 7,
		FollowUps: []domain.FollowUpInput{{Kind: domain.KindPromise, Text: "Ligar na sexta"}}}}
	s.svc.WithSuggester(fake)
	mux := s.mux()
	mine, _ := s.conversation(&s.agent)
	others, _ := s.conversation(&s.env.UserA)
	unassigned, _ := s.conversation(nil)
	sug := func(user uuid.UUID, conv uuid.UUID) (int, map[string]any) {
		return s.call(mux, user, "POST", fmt.Sprintf("/inbox/conversations/%s/finalize/suggest", conv), "")
	}

	if code, _ := sug(uuid.Nil, mine); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	if code, _ := sug(s.observer, mine); code != http.StatusForbidden {
		t.Fatalf("no claim/manage: %d", code)
	}
	if code, _ := sug(s.agent, others); code != http.StatusForbidden {
		t.Fatalf("somebody else's conversation: %d", code)
	}
	if code, _ := sug(s.agent, unassigned); code != http.StatusForbidden {
		t.Fatalf("an unassigned one: %d", code)
	}
	if fake.calls != 0 {
		t.Fatal("the model is never asked on behalf of someone who cannot finalize")
	}
	if code, _ := sug(s.agent, uuid.New()); code != http.StatusNotFound {
		t.Fatalf("unknown: %d", code)
	}
	code, out := sug(s.agent, mine)
	if code != http.StatusOK || out["summary"] != "Link voltou." || out["summary_truth"] != "ai_inferred" || out["model"] != "mini" || out["based_on_messages"] != float64(7) {
		t.Fatalf("suggest: %d %v", code, out)
	}
	if items := out["follow_ups"].([]any); len(items) != 1 || items[0].(map[string]any)["kind"] != "promise" {
		t.Fatalf("items: %v", out["follow_ups"])
	}
	if code, _ := sug(s.env.UserA, unassigned); code != http.StatusOK {
		t.Fatalf("a supervisor may ask for an unassigned one: %d", code)
	}
	// nothing was stored or changed by suggesting
	if s.count(`SELECT count(*) FROM conversation_closures`) != 0 || s.count(`SELECT count(*) FROM conversations WHERE id=$1 AND status='open'`, mine) != 1 {
		t.Fatal("a suggestion is a draft: nothing is recorded")
	}
	// degraded modes never block finalizing by hand
	for name, tc := range map[string]struct {
		err  error
		code int
		body string
	}{
		"disabled":    {domain.ErrSuggestionDisabled, http.StatusServiceUnavailable, "ai_disabled"},
		"unavailable": {domain.ErrSuggestionUnavailable, http.StatusServiceUnavailable, "ai_unavailable"},
		"nothing":     {domain.ErrNothingToSuggest, http.StatusUnprocessableEntity, "nothing_to_suggest"},
	} {
		fake.err = tc.err
		if code, out := sug(s.agent, mine); code != tc.code || out["error"] != tc.body {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	fake.err = nil
	// the model call holds NO row lock: while it runs, the conversation can still be changed by someone else
	slow := &fakeSuggester{out: fake.out, hold: make(chan struct{}), seen: make(chan struct{})}
	release := sync.OnceFunc(func() { close(slow.hold) })
	defer release() // even when an assertion fails below: a stuck model call would keep a transaction open and hang the cleanup
	s.svc.WithSuggester(slow)
	done := make(chan int, 1)
	go func() { code, _ := sug(s.agent, mine); done <- code }()
	<-slow.seen
	updated := make(chan error, 1)
	go func() {
		_, err := s.env.Seed.Exec(context.Background(), `UPDATE conversations SET title='editado durante a sugestão' WHERE id=$1`, mine)
		updated <- err
	}()
	select {
	case err := <-updated:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the suggestion is holding a lock on the conversation while the model runs")
	}
	release()
	if code := <-done; code != http.StatusOK {
		t.Fatalf("slow suggest: %d", code)
	}
	// a finalized conversation has nothing left to suggest
	var res application.FinalizeResult
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) { res, _ = s.svc.Finalize(ctx, finalizeInput(mine)) })
	if res.Closure == nil {
		t.Fatal("setup: finalize failed")
	}
	if code, out := sug(s.agent, mine); code != http.StatusUnprocessableEntity || out["error"] != "nothing_to_suggest" {
		t.Fatalf("finalized: %d %v", code, out)
	}
	// without a suggester the feature simply is not offered
	s2 := newStack(t)
	conv2, _ := s2.conversation(&s2.agent)
	if code, out := s2.call(s2.mux(), s2.agent, "POST", fmt.Sprintf("/inbox/conversations/%s/finalize/suggest", conv2), ""); code != http.StatusServiceUnavailable || out["error"] != "ai_disabled" {
		t.Fatalf("no suggester configured: %d %v", code, out)
	}
}
