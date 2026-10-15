package adapters_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/omnira/omnira/internal/servicectx/adapters"
)

// Drift guard between contracts/openapi/service-context-v1.yaml and the routes the handler registers, in BOTH directions.

var (
	specPath   = regexp.MustCompile(`^  (/\S+):\s*$`)
	specMethod = regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
	paramRE    = regexp.MustCompile(`\{[^}]+\}`)
)

type recorder struct{ patterns []string }

func (r *recorder) Handle(pattern string, _ http.Handler) { r.patterns = append(r.patterns, pattern) }

func TestOpenAPIAndRoutesAgree(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/openapi/service-context-v1.yaml")
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
	adapters.NewHandler(nil, nil).WithExternalOrders(true).Routes(rec, func(fn http.HandlerFunc) http.Handler { return fn }, nil)
	routed := map[string]bool{}
	for _, p := range rec.patterns {
		routed[paramRE.ReplaceAllString(p, "{}")] = true
	}
	if len(documented) == 0 || len(routed) == 0 {
		t.Fatalf("nothing found: documented=%d routed=%d", len(documented), len(routed))
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
	adapters.NewHandler(nil, nil).WithExternalOrders(true).Routes(mux, func(fn http.HandlerFunc) http.Handler { return fn }, nil)
	for d := range documented {
		parts := strings.SplitN(d, " ", 2)
		concrete := paramRE.ReplaceAllString(parts[1], "3f9c1b2e-0000-4000-8000-000000000001")
		if _, pattern := mux.Handler(httptest.NewRequest(parts[0], concrete, nil)); pattern == "" {
			t.Errorf("no ServeMux route for %s", d)
		}
	}
}

// Semantic guard over the spec text: every change to an existing record declares its precondition (If-Match, 412, 428) and every answer that returns a record
// declares the ETag the next change must echo. The route test above only proves the routes exist.
func TestOpenAPIDeclaresTheConcurrencyContract(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/openapi/service-context-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	opStart := regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
	var ops []struct{ name, text string }
	var cur *struct{ name, text string }
	inPaths := false
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case line == "paths:":
			inPaths = true
		case line == "components:":
			inPaths = false
			if cur != nil {
				ops = append(ops, *cur)
				cur = nil
			}
		case inPaths && (opStart.MatchString(line) || specPath.MatchString(line)):
			if cur != nil {
				ops = append(ops, *cur)
				cur = nil
			}
			if opStart.MatchString(line) {
				cur = &struct{ name, text string }{name: line}
			}
		case inPaths && cur != nil:
			cur.text += line + "\n"
			if m := regexp.MustCompile(`operationId: (\w+)`).FindStringSubmatch(line); m != nil {
				cur.name = m[1]
			}
		}
	}
	if len(ops) < 25 {
		t.Fatalf("parsed only %d operations", len(ops))
	}
	record := regexp.MustCompile(`'(200|201)': \{[^\n]*schema: \{ \$ref: '#/components/schemas/(Session|Case|Task|ExternalRecord|ExternalRequest)' \}`)
	for _, op := range ops {
		if strings.Contains(op.text, "parameters/IfMatch") {
			for _, code := range []string{"'412'", "'428'"} {
				if !strings.Contains(op.text, code) {
					t.Errorf("%s changes an existing record with If-Match but does not declare %s", op.name, code)
				}
			}
		}
		for _, line := range strings.Split(op.text, "\n") {
			if record.MatchString(line) && !strings.Contains(line, "ETag") {
				t.Errorf("%s returns a record without declaring its ETag: %s", op.name, strings.TrimSpace(line))
			}
		}
	}
}
