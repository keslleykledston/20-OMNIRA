package adapters_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/attendance/adapters"
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
