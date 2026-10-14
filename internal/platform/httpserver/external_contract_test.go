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
