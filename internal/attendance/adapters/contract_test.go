package adapters_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/omnira/omnira/internal/attendance/adapters"
)

// Drift guard between contracts/openapi/attendance-v1.yaml and the routes the handler registers, in BOTH directions.

var (
	specPath   = regexp.MustCompile(`^  (/\S+):\s*$`)
	specMethod = regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
	paramRE    = regexp.MustCompile(`\{[^}]+\}`)
)

type recorder struct{ patterns []string }

func (r *recorder) Handle(pattern string, _ http.Handler) { r.patterns = append(r.patterns, pattern) }

func TestOpenAPIAndRoutesAgree(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/openapi/attendance-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	inPaths, cur := false, ""
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line == "components:":
			inPaths = false
		case inPaths:
			if m := specPath.FindStringSubmatch(line); m != nil {
				cur = m[1]
			} else if m := specMethod.FindStringSubmatch(line); m != nil && cur != "" {
				documented[strings.ToUpper(m[1])+" /api/v1"+paramRE.ReplaceAllString(cur, "{}")] = true
			}
		}
	}
	rec := &recorder{}
	adapters.NewHandler(nil).Routes(rec, func(fn http.HandlerFunc) http.Handler { return fn })
	routed := map[string]bool{}
	for _, p := range rec.patterns {
		routed[paramRE.ReplaceAllString(p, "{}")] = true
	}
	if len(documented) != 5 || len(routed) != 5 {
		t.Fatalf("expected 5 operations: documented=%d routed=%d", len(documented), len(routed))
	}
	var problems []string
	for d := range documented {
		if !routed[d] {
			problems = append(problems, "documented but not routed: "+d)
		}
	}
	for r := range routed {
		if !documented[r] {
			problems = append(problems, "routed but not documented: "+r)
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
	mux := http.NewServeMux()
	adapters.NewHandler(nil).Routes(mux, func(fn http.HandlerFunc) http.Handler { return fn })
	for d := range documented {
		parts := strings.SplitN(d, " ", 2)
		concrete := paramRE.ReplaceAllString(parts[1], "3f9c1b2e-0000-4000-8000-000000000001")
		if _, pattern := mux.Handler(httptest.NewRequest(parts[0], concrete, nil)); pattern == "" {
			t.Errorf("no ServeMux route for %s", d)
		}
	}
}
