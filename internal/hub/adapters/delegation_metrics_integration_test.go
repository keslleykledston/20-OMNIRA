package adapters_test

// Operational signal of the delegated serving core (docs/architecture/OBSERVABILITY.md): how requests that act for a Hub end, by outcome, in /metrics. The labels are a
// closed set (never a company, person or hub), so an alert on "refused" or "forbidden_key" jumping cannot become a cardinality problem.

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/omnira/omnira/internal/platform/opsmetrics"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
)

func delegationCount(outcome string) int {
	re := regexp.MustCompile(`omnira_hub_delegation_requests_total\{outcome="` + outcome + `"\} (\d+)`)
	m := re.FindStringSubmatch(opsmetrics.Render())
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func TestDelegatedServingOutcomesAreCounted(t *testing.T) {
	w := newWorld(t)
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })

	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "conversation.read")
	w.grantKeys(g, "conversation.read")
	keyless := w.hubAgent("keyless") // a live grant that does not hold the key the route needs
	gk := w.grant(keyless, "A")
	w.grantKeys(gk, "contact.read")
	w.ceiling("A", "conversation.read", "contact.read")
	stranger := w.user("stranger")
	hubHeader := "hub:" + w.hub.String()
	h := w.servedChain(&servedProbe{}, "conversation.read")

	expect := func(outcome string, do func() int, want int) {
		t.Helper()
		before := delegationCount(outcome)
		if code := do(); code != want {
			t.Fatalf("%s: code = %d, want %d", outcome, code, want)
		}
		if got := delegationCount(outcome); got != before+1 {
			t.Errorf("%s counter: %d -> %d, want +1", outcome, before, got)
		}
	}
	expect("served", func() int { return w.serve(h, agent, "A", hubHeader) }, http.StatusOK)
	expect("refused", func() int { return w.serve(h, stranger, "A", hubHeader) }, http.StatusNotFound)
	expect("forbidden_key", func() int { return w.serve(h, keyless, "A", hubHeader) }, http.StatusForbidden)
	t.Run("switched off", func(t *testing.T) {
		tenancyadapters.EnableDelegatedServing(false)
		defer tenancyadapters.EnableDelegatedServing(true)
		expect("disabled", func() int { return w.serve(h, agent, "A", hubHeader) }, http.StatusForbidden)
	})
	// a member's own request never counts as delegation
	member := w.user("member")
	w.directMember(member, "A")
	before := delegationCount("served")
	if code := w.serve(h, member, "A", ""); code != http.StatusOK {
		t.Fatalf("member: %d", code)
	}
	if got := delegationCount("served"); got != before {
		t.Errorf("a member's request counted as delegated: %d -> %d", before, got)
	}
	if out := opsmetrics.Render(); strings.Contains(out, w.hub.String()) || strings.Contains(out, w.tenant["A"].String()) {
		t.Error("an id of a hub or company reached /metrics")
	}
}
