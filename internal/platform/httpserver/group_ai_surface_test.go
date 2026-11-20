package httpserver

import (
	"net/http"
	"testing"

	groupaiadapters "github.com/omnira/omnira/internal/groupai/adapters"
)

// ADR-0060: every route of the group copilot answers 401 to an anonymous caller and to a forged credential, before any handler code runs.
func TestGroupAIRoutesRejectAnonymousAndForgedCredentials(t *testing.T) {
	s := newRoutedServer(t)
	s.RegisterGroupAIHandlers(nil, groupaiadapters.NewHandler(nil, nil, nil))
	s.SetupRateLimiting()
	s.SetupRequestMeta()
	ops := opsOfSpec(t, "../../../contracts/openapi/group-ai-v1.yaml")
	if len(ops) != 19 {
		t.Fatalf("expected 19 group copilot operations, parsed %d", len(ops))
	}
	for _, o := range ops {
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
