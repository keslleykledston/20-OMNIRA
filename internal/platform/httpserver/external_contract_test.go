package httpserver

import (
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	externaladapters "github.com/omnira/omnira/internal/external/adapters"
)

// Gate 20 of ADR-0046: the external contract and the routes cannot drift. Forward: every operation documented in omnira-external-v1.yaml is a
// registered route (real ServeMux matching). Reverse: every /api/v1/external route registered in server.go is documented. Removing a route or a
// documented operation fails one of the two.

func externalSpecOps(t *testing.T) []op {
	t.Helper()
	raw, err := os.ReadFile("../../../contracts/openapi/omnira-external-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var ops []op
	inPaths, cur := false, ""
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "paths:" {
			inPaths = true
			continue
		}
		if inPaths && line == "components:" {
			break
		}
		if !inPaths {
			continue
		}
		if m := specPathRE.FindStringSubmatch(line); m != nil {
			cur = m[1]
			continue
		}
		if m := specMethodRE.FindStringSubmatch(line); m != nil && cur != "" {
			ops = append(ops, op{strings.ToUpper(m[1]), "/api/v1" + cur})
		}
	}
	if len(ops) == 0 {
		t.Fatal("no operations parsed from the external contract")
	}
	return ops
}

func TestEveryDocumentedExternalOperationIsARegisteredRoute(t *testing.T) {
	s := New("127.0.0.1:0")
	s.RegisterExternalHandlers(nil, true, externaladapters.Features{})
	for _, o := range externalSpecOps(t) {
		concrete := paramRE.ReplaceAllString(o.path, "3f9c1b2e-0000-4000-8000-000000000001")
		_, pattern := s.mux.Handler(httptest.NewRequest(o.method, concrete, nil))
		if pattern == "" {
			t.Errorf("documented but not routed: %s %s", o.method, o.path)
		}
	}
}

func TestEveryRegisteredExternalRouteIsDocumented(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for _, o := range externalSpecOps(t) {
		documented[o.method+" "+paramRE.ReplaceAllString(o.path, "{}")] = true
	}
	re := regexp.MustCompile(`s\.mux\.Handle\("(GET|POST|PUT|PATCH|DELETE) (/api/v1/external[^"]*)"`)
	var found []string
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		found = append(found, m[1]+" "+paramRE.ReplaceAllString(m[2], "{}"))
	}
	if len(found) == 0 {
		t.Fatal("no /api/v1/external route found in server.go — the extraction is broken")
	}
	sort.Strings(found)
	for _, f := range found {
		if !documented[f] {
			t.Errorf("routed but not documented in contracts/openapi/omnira-external-v1.yaml: %s", f)
		}
	}
}

// With the flag off the surface does not exist at all.
func TestExternalRoutesDoNotExistWhenHeadlessIsOff(t *testing.T) {
	s := New("127.0.0.1:0")
	s.RegisterExternalHandlers(nil, false, externaladapters.Features{})
	for _, o := range externalSpecOps(t) {
		if _, pattern := s.mux.Handler(httptest.NewRequest(o.method, strings.ReplaceAll(o.path, "{", "x"), nil)); pattern != "" {
			t.Errorf("%s %s is routed with OMNIRA_HEADLESS_ENABLED off", o.method, o.path)
		}
	}
}

// The administration routes (omnira-v1.yaml, tag ExternalApps) are routed exactly as documented, in both directions (Codex H1).
func TestEveryRegisteredExternalAdminRouteIsDocumented(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "func (s *Server) RegisterExternalHandlers")
	j := strings.Index(body, "func (s *Server) RegisterInvitationHandlers")
	if i < 0 || j < i {
		t.Fatal("RegisterExternalHandlers not found")
	}
	documented := map[string]bool{}
	for _, o := range specOps(t) {
		documented[o.method+" "+paramRE.ReplaceAllString(o.path, "{}")] = true
	}
	re := regexp.MustCompile(`route\("(GET|POST|PUT|PATCH|DELETE) "\+appsPath(?:\+"([^"]*)")?`)
	found := 0
	for _, m := range re.FindAllStringSubmatch(body[i:j], -1) {
		found++
		key := m[1] + " " + paramRE.ReplaceAllString("/api/v1/tenants/{tenant_id}/integrations/apps"+m[2], "{}")
		if !documented[key] {
			t.Errorf("routed but not documented in contracts/openapi/omnira-v1.yaml: %s", key)
		}
	}
	if found < 9 {
		t.Fatalf("parsed %d admin routes, expected 9 — the extraction is broken", found)
	}
	// and the other way round: everything documented under integrations/apps is one of the registered routes
	registered := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(body[i:j], -1) {
		registered[m[1]+" "+paramRE.ReplaceAllString("/api/v1/tenants/{tenant_id}/integrations/apps"+m[2], "{}")] = true
	}
	var extra []string
	for _, o := range specOps(t) {
		k := o.method + " " + paramRE.ReplaceAllString(o.path, "{}")
		if strings.Contains(o.path, "/integrations/apps") && !registered[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("documented under integrations/apps but not registered: %v", extra)
	}
}
