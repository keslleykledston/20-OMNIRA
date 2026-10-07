package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	attendanceadapters "github.com/omnira/omnira/internal/attendance/adapters"
	flowsadapters "github.com/omnira/omnira/internal/flows/adapters"
)

// MOBILE.READINESS security net. Any client (web today, Android/iOS tomorrow) reaches the API through the same routes, so the
// guarantee "no tenant data without a verified identity" must hold for EVERY documented tenant route, not only the ones somebody
// remembered to test. The server is built exactly as the contract test builds it (nil dependencies): a route that reached its
// handler, or its database, before authenticating would panic or answer something other than 401 here.

func protectedOps(t *testing.T) []op {
	var out []op
	for _, o := range specOps(t) {
		p := strings.TrimPrefix(o.path, "/api/v1")
		if strings.HasPrefix(p, "/tenants/") || p == "/tenants" || p == "/me" || strings.HasPrefix(p, "/me/") {
			out = append(out, o)
		}
	}
	if len(out) < 80 {
		t.Fatalf("only %d protected operations found: the contract parser or the path rule broke", len(out))
	}
	return out
}

func requestVia(t *testing.T, h http.Handler, o op, mutate func(*http.Request)) (rr *httptest.ResponseRecorder, panicked any) {
	t.Helper()
	concrete := paramRE.ReplaceAllString(o.path, "3f9c1b2e-0000-4000-8000-000000000001")
	req := httptest.NewRequest(o.method, concrete, strings.NewReader("{}"))
	if mutate != nil {
		mutate(req)
	}
	rr = httptest.NewRecorder()
	func() {
		defer func() { panicked = recover() }()
		h.ServeHTTP(rr, req)
	}()
	return rr, panicked
}

func TestEveryProtectedRouteRejectsAnonymousAndForgedCredentials(t *testing.T) {
	s := newRoutedServer(t)
	s.SetupRateLimiting()
	s.SetupRequestMeta()
	h := s.srv.Handler
	cases := map[string]func(*http.Request){
		"no credential":            nil,
		"garbage bearer":           func(r *http.Request) { r.Header.Set("Authorization", "Bearer not-a-jwt") },
		"malformed authorization":  func(r *http.Request) { r.Header.Set("Authorization", "Token abc") },
		"empty bearer":             func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") },
		"client-supplied tenant":   func(r *http.Request) { r.Header.Set("X-Tenant-ID", "3f9c1b2e-0000-4000-8000-000000000001") },
		"client-supplied identity": func(r *http.Request) { r.Header.Set("X-User-ID", "3f9c1b2e-0000-4000-8000-000000000002") },
	}
	for _, o := range protectedOps(t) {
		for name, mutate := range cases {
			rr, p := requestVia(t, h, o, mutate)
			if p != nil {
				t.Errorf("%s %s [%s]: handler panicked before authenticating: %v", o.method, o.path, name, p)
				continue
			}
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s [%s]: status %d, want 401 (a route answers without a verified identity)", o.method, o.path, name, rr.Code)
			}
			if rr.Header().Get("X-Request-ID") == "" {
				t.Errorf("%s %s [%s]: no X-Request-ID", o.method, o.path, name)
			}
		}
	}
}

// The same rejection, as a mobile client sees it: stable machine-readable envelope with the request id.
func TestProtectedRoutesAnswerTheStableEnvelopeWhenAsked(t *testing.T) {
	s := newRoutedServer(t)
	s.SetupRateLimiting()
	s.SetupRequestMeta()
	for _, o := range protectedOps(t) {
		rr, p := requestVia(t, s.srv.Handler, o, func(r *http.Request) {
			r.Header.Set("Accept", MediaTypeV1)
			r.Header.Set("X-Request-ID", "mobile-readiness-1")
		})
		if p != nil {
			t.Errorf("%s %s: panic %v", o.method, o.path, p)
			continue
		}
		var env errorEnvelope
		if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil || rr.Code != 401 ||
			env.Error.Code != "UNAUTHENTICATED" || env.Error.RequestID != "mobile-readiness-1" || rr.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s %s: %d %q (%v)", o.method, o.path, rr.Code, rr.Body.String(), err)
		}
	}
}

// The attendance (ADR-0020) and flow (ADR-0019) contracts live in their own files, so the loop above does not see them.
// Same guarantee, same server wiring, other contracts.
func opsOfSpec(t *testing.T, file string) []op {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var ops []op
	inPaths, cur := false, ""
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line == "components:":
			return ops
		case inPaths:
			if m := specPathRE.FindStringSubmatch(line); m != nil {
				cur = m[1]
			} else if m := specMethodRE.FindStringSubmatch(line); m != nil && cur != "" {
				ops = append(ops, op{strings.ToUpper(m[1]), "/api/v1" + cur})
			}
		}
	}
	return ops
}

func TestAttendanceAndFlowRoutesRejectAnonymousAndForgedCredentials(t *testing.T) {
	s := newRoutedServer(t)
	s.RegisterAttendanceHandlers(nil, attendanceadapters.NewHandler(nil))
	s.RegisterFlowHandlers(nil, flowsadapters.NewHandler(nil, nil))
	s.SetupRateLimiting()
	s.SetupRequestMeta()
	n := 0
	for _, file := range []string{"../../../contracts/openapi/attendance-v1.yaml", "../../../contracts/openapi/flows-v1.yaml"} {
		ops := opsOfSpec(t, file)
		if len(ops) < 6 {
			t.Fatalf("%s: only %d operations parsed", file, len(ops))
		}
		for _, o := range ops {
			n++
			for name, mutate := range map[string]func(*http.Request){
				"no credential":  nil,
				"garbage bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer not-a-jwt") },
			} {
				rr, p := requestVia(t, s.srv.Handler, o, mutate)
				if p != nil {
					t.Errorf("%s %s [%s]: panicked before authenticating: %v", o.method, o.path, name, p)
				} else if rr.Code != http.StatusUnauthorized {
					t.Errorf("%s %s [%s]: status %d, want 401", o.method, o.path, name, rr.Code)
				}
			}
		}
	}
	if n < 28 {
		t.Fatalf("expected at least 28 attendance+flow operations, saw %d", n)
	}
}
