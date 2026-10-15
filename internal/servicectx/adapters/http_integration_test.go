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
	"time"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/servicectx/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func (s *stack) mux() *http.ServeMux { return muxFor(s, s.handler) }

func muxFor(s *stack, h *adapters.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	h.Routes(mux, func(fn http.HandlerFunc) http.Handler {
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
	}, nil)
	return mux
}

type reply struct {
	code   int
	body   map[string]any
	header http.Header
}

func (s *stack) call(mux *http.ServeMux, user uuid.UUID, method, path, raw string, headers ...string) reply {
	s.t.Helper()
	req := httptest.NewRequest(method, fmt.Sprintf("/api/v1/tenants/%s%s", s.env.TenantA, path), bytes.NewReader([]byte(raw)))
	if user != uuid.Nil {
		req.Header.Set("X-Test-User", user.String())
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return reply{rec.Code, out, rec.Header()}
}

func TestHTTPSessionLifecycle(t *testing.T) {
	s := newStack(t)
	mux := s.mux()
	conv, _ := s.conversation()
	body := fmt.Sprintf(`{"conversation_id":"%s"}`, conv)

	if r := s.call(mux, uuid.Nil, "POST", "/service-sessions", body); r.code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", r.code)
	}
	if r := s.call(mux, s.observer, "POST", "/service-sessions", body); r.code != http.StatusForbidden || r.body["error"] != "forbidden" {
		t.Fatalf("no service.create: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "POST", "/service-sessions", `{"conversation_id":"`+conv.String()+`","tenant_id":"`+uuid.NewString()+`"}`); r.code != http.StatusBadRequest {
		t.Fatalf("a smuggled tenant_id (unknown field) must be refused: %d", r.code)
	}
	if r := s.call(mux, s.agent, "POST", "/service-sessions", `{"conversation_id":"`+conv.String()+`","opened_source":"flow"}`); r.code != http.StatusBadRequest {
		t.Fatalf("the caller cannot pick the source: %d", r.code)
	}
	key := "http-key-" + uuid.NewString()
	r := s.call(mux, s.agent, "POST", "/service-sessions", body, "Idempotency-Key", key)
	if r.code != http.StatusCreated || r.body["protocol"] != s.proto(s.env.TenantA, 1001) || r.header.Get("ETag") != `"1"` || r.body["opened_source"] != "operator" {
		t.Fatalf("create: %d %v %v", r.code, r.body, r.header)
	}
	id := r.body["id"].(string)
	if _, leaked := r.body["tenant_id"]; leaked {
		t.Fatal("the response never exposes the tenant")
	}
	again := s.call(mux, s.agent, "POST", "/service-sessions", body, "Idempotency-Key", key)
	if again.code != http.StatusOK || again.header.Get("Idempotent-Replay") != "true" || again.body["id"] != id {
		t.Fatalf("replay: %d %v", again.code, again.header)
	}
	other, _ := s.conversation()
	if r := s.call(mux, s.agent, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, other), "Idempotency-Key", key); r.code != http.StatusUnprocessableEntity || r.body["error"] != "idempotency_key_reused" {
		t.Fatalf("same key, other request: %d %v", r.code, r.body)
	}

	path := "/service-sessions/" + id
	if r := s.call(mux, s.agent, "POST", path+"/status", `{"status":"in_progress"}`); r.code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", r.code)
	}
	if r := s.call(mux, s.agent, "POST", path+"/status", `{"status":"in_progress"}`, "If-Match", `"9"`); r.code != http.StatusPreconditionFailed || r.body["error"] != "version_conflict" {
		t.Fatalf("stale: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "POST", path+"/status", `{"status":"closed"}`, "If-Match", `"1"`); r.code != http.StatusBadRequest {
		t.Fatalf("closing goes through /close: %d", r.code)
	}
	r = s.call(mux, s.agent, "POST", path+"/status", `{"status":"waiting"}`, "If-Match", `"1"`)
	if r.code != http.StatusOK || r.body["status"] != "waiting" || r.header.Get("ETag") != `"2"` {
		t.Fatalf("status: %d %v %v", r.code, r.body, r.header)
	}
	if r := s.call(mux, s.agent, "POST", path+"/close", `{"resolution_code":"inexistente"}`, "If-Match", `"2"`); r.code != http.StatusUnprocessableEntity || r.body["error"] != "unknown_result" {
		t.Fatalf("unknown result: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.limited, "POST", path+"/close", `{"resolution_code":"other"}`, "If-Match", `"2"`); r.code != http.StatusForbidden {
		t.Fatalf("no service.close: %d", r.code)
	}
	r = s.call(mux, s.agent, "POST", path+"/close", `{"resolution_code":"information_provided","note":"só o horário"}`, "If-Match", `"2"`)
	if r.code != http.StatusOK || r.body["status"] != "closed" || r.body["resolution_canonical"] != "information_provided" {
		t.Fatalf("close: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "POST", path+"/status", `{"status":"waiting"}`, "If-Match", `"3"`); r.code != http.StatusConflict || r.body["error"] != "invalid_transition" {
		t.Fatalf("closed is final for status moves: %d %v", r.code, r.body)
	}
	r = s.call(mux, s.agent, "POST", path+"/reopen", `{"reason":"o cliente voltou"}`, "If-Match", `"3"`)
	if r.code != http.StatusOK || r.body["status"] != "in_progress" || r.body["reopen_count"] != float64(1) {
		t.Fatalf("reopen: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "POST", path+"/reopen", `{"reason":""}`, "If-Match", `"4"`); r.code != http.StatusBadRequest {
		t.Fatalf("a reason is required: %d", r.code)
	}
	if r := s.call(mux, s.agent, "GET", "/conversations/"+conv.String()+"/service-session", ""); r.code != http.StatusOK || r.body["id"] != id {
		t.Fatalf("by conversation: %d %v", r.code, r.body)
	}
	empty, _ := s.conversation()
	if r := s.call(mux, s.agent, "GET", "/conversations/"+empty.String()+"/service-session", ""); r.code != http.StatusNotFound || r.body["error"] != "no_session" {
		t.Fatalf("a conversation without a session: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "GET", "/service-sessions/not-a-uuid", ""); r.code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", r.code)
	}
	if r := s.call(mux, s.env.UserB, "GET", path, ""); r.code != http.StatusNotFound && r.code != http.StatusForbidden {
		t.Fatalf("another company's person: %d", r.code)
	}
}

func TestHTTPListAndMessagesPaginate(t *testing.T) {
	s := newStack(t)
	mux := s.mux()
	conv, _ := s.conversation()
	r := s.call(mux, s.agent, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, conv))
	id := r.body["id"].(string)
	for i := 0; i < 5; i++ {
		time.Sleep(3 * time.Millisecond)
		s.message(conv, "inbound", time.Now().UTC(), nil)
	}
	seen, cursor := 0, ""
	for page := 0; page < 10; page++ {
		q := "?limit=2"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		r := s.call(mux, s.agent, "GET", "/service-sessions/"+id+"/messages"+q, "")
		if r.code != http.StatusOK {
			t.Fatalf("messages: %d %v", r.code, r.body)
		}
		seen += len(r.body["items"].([]any))
		next, _ := r.body["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if seen != 5 {
		t.Fatalf("every message exactly once across pages: %d", seen)
	}
	if r := s.call(mux, s.agent, "GET", "/service-sessions?limit=1&status=bogus", ""); r.code != http.StatusBadRequest {
		t.Fatalf("a bad status filter: %d", r.code)
	}
	if r := s.call(mux, s.agent, "GET", "/service-sessions?cursor=%%%", ""); r.code != http.StatusBadRequest && r.code != http.StatusOK {
		t.Fatalf("a bad cursor must not be a 500: %d", r.code)
	}
	if r := s.call(mux, s.agent, "GET", "/service-sessions?cursor=bm90LWEtY3Vyc29y", ""); r.code != http.StatusBadRequest {
		t.Fatalf("an undecodable cursor is a 400: %d", r.code)
	}
	if r := s.call(mux, s.agent, "GET", "/service-sessions?limit=5", ""); r.code != http.StatusOK || len(r.body["items"].([]any)) != 1 {
		t.Fatalf("list: %d %v", r.code, r.body)
	}
}

func TestHTTPCatalogsAreForCompanyAdministrators(t *testing.T) {
	s := newStack(t)
	mux := s.mux()
	if r := s.call(mux, s.agent, "POST", "/service-categories", `{"code":"internet","label":"Internet"}`); r.code != http.StatusForbidden {
		t.Fatalf("an attendant does not configure the company: %d", r.code)
	}
	r := s.call(mux, s.admin, "POST", "/service-categories", `{"code":"internet","label":"Internet"}`)
	if r.code != http.StatusCreated || r.body["code"] != "internet" {
		t.Fatalf("admin creates: %d %v", r.code, r.body)
	}
	if r2 := s.call(mux, s.admin, "POST", "/service-categories", `{"code":"internet","label":"Outra"}`); r2.code != http.StatusBadRequest {
		t.Fatalf("a duplicate code: %d", r2.code)
	}
	if r2 := s.call(mux, s.admin, "POST", "/service-categories", `{"code":"Bad Code","label":"x"}`); r2.code != http.StatusBadRequest {
		t.Fatalf("a malformed code: %d", r2.code)
	}
	cat := r.body["id"].(string)
	if r2 := s.call(mux, s.admin, "PATCH", "/service-categories/"+cat, `{"active":false}`); r2.code != http.StatusOK || r2.body["active"] != false {
		t.Fatalf("deactivate: %d %v", r2.code, r2.body)
	}
	if r2 := s.call(mux, s.agent, "GET", "/service-categories", ""); r2.code != http.StatusOK || len(r2.body["items"].([]any)) != 0 {
		t.Fatalf("an inactive category is hidden from the list: %d %v", r2.code, r2.body)
	}
	// an inactive category cannot be used to classify
	conv, _ := s.conversation()
	rs := s.call(mux, s.agent, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s","category_id":"%s"}`, conv, cat))
	if rs.code != http.StatusBadRequest {
		t.Fatalf("classifying with an inactive category: %d %v", rs.code, rs.body)
	}
	if r2 := s.call(mux, s.admin, "POST", "/service-result-codes", `{"code":"other","canonical_code":"other","label":"x"}`); r2.code != http.StatusBadRequest {
		t.Fatalf("a built-in name is taken: %d", r2.code)
	}
	if r2 := s.call(mux, s.admin, "POST", "/service-result-codes", `{"code":"segunda_via","canonical_code":"information_provided","label":"Segunda via"}`); r2.code != http.StatusCreated {
		t.Fatalf("a company code: %d %v", r2.code, r2.body)
	}
	lst := s.call(mux, s.agent, "GET", "/service-result-codes", "")
	if lst.code != http.StatusOK || len(lst.body["items"].([]any)) != 1 || !strings.Contains(fmt.Sprint(lst.body["built_in"]), "resolved_first_contact") {
		t.Fatalf("result codes: %d %v", lst.code, lst.body)
	}
}
