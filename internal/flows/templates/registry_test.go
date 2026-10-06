package templates_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/templates"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func registry(t *testing.T) *templates.Registry {
	t.Helper()
	r, err := templates.Default()
	if err != nil {
		t.Fatalf("the template library does not build: %v", err)
	}
	return r
}

func syntheticMapping(tpl *templates.Template) map[string]string {
	m := map[string]string{}
	for _, d := range tpl.Mappings {
		m[d.Key] = uuid.NewSHA1(uuid.NameSpaceOID, []byte("test-"+d.Key)).String()
	}
	return m
}

func TestEveryTemplateIsValidAndSelfDescribing(t *testing.T) {
	r := registry(t)
	for _, tpl := range r.AllVersions() {
		def, err := domain.ParseDefinition(tpl.Definition)
		if err != nil {
			t.Errorf("%s@%d does not parse: %v", tpl.Slug, tpl.Version, err)
			continue
		}
		// With placeholders allowed it must have NO errors (warnings only), and not a single secret.
		if issues := domain.Validate(def, domain.ValidateOptions{AllowPlaceholders: true}); domain.HasErrors(issues) {
			t.Errorf("%s@%d has blocking errors: %+v", tpl.Slug, tpl.Version, issues)
		}
		if tpl.Version < 1 || tpl.Name == "" || tpl.Description == "" || len(tpl.Categories) == 0 || tpl.Type == "" {
			t.Errorf("%s: incomplete metadata", tpl.Slug)
		}
		// Placeholders are documented and no tenant id / real uuid is hard-coded in a template.
		if strings.Contains(string(tpl.Definition), "-") && hasHardcodedUUID(string(tpl.Definition)) {
			t.Errorf("%s hard-codes a uuid: templates must use placeholders", tpl.Slug)
		}
		if len(tpl.Definition) > 200<<10 {
			t.Errorf("%s is unreasonably large (%d bytes)", tpl.Slug, len(tpl.Definition))
		}
		if len(tpl.Tests) == 0 {
			t.Errorf("%s has no test cases: every system template ships with Given/When/Then tests", tpl.Slug)
		}
		// Only flows meant to start conversations can be INBOUND defaults; subflows and surveys never are.
		if tpl.Type != domain.FlowTypeInbound && tpl.Settings.IsDefault {
			t.Errorf("%s: only an INBOUND template can be the default flow", tpl.Slug)
		}
		for _, dep := range tpl.Requires {
			d, ok := r.Template(dep, 0)
			if !ok {
				t.Errorf("%s calls subflow %q which is not in the library", tpl.Slug, dep)
			} else if d.Type != domain.FlowTypeSubflow {
				t.Errorf("%s calls %q which is %s, not a SUBFLOW", tpl.Slug, dep, d.Type)
			}
		}
		// Resolving with synthetic resources yields a definition with zero placeholders that validates strictly.
		resolved, err := templates.Resolve(tpl.Definition, syntheticMapping(tpl), nil)
		if err != nil {
			t.Errorf("%s: %v", tpl.Slug, err)
			continue
		}
		if keys := templates.PlaceholderKeys(resolved); len(keys) != 0 {
			t.Errorf("%s: placeholders left after resolving: %v", tpl.Slug, keys)
		}
		rd, _ := domain.ParseDefinition(resolved)
		if issues := domain.Validate(rd, domain.ValidateOptions{}); domain.HasErrors(issues) {
			t.Errorf("%s resolved has errors: %+v", tpl.Slug, issues)
		}
	}
}

func hasHardcodedUUID(s string) bool {
	for i := 0; i+36 <= len(s); i++ {
		if _, err := uuid.Parse(s[i : i+36]); err == nil {
			return true
		}
	}
	return false
}

func TestPublishedVersionsAreImmutable(t *testing.T) {
	r := registry(t)
	current := map[string]string{}
	for _, tpl := range r.AllVersions() {
		current[fmt.Sprintf("template:%s@%d", tpl.Slug, tpl.Version)] = tpl.Hash()
	}
	for _, p := range r.AllPackVersions() {
		current[fmt.Sprintf("pack:%s@%d", p.Slug, p.Version)] = p.Hash()
	}
	const golden = "testdata/hashes.json"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		raw, _ := json.MarshalIndent(current, "", "  ")
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Skip("golden updated")
	}
	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing %s (run once with UPDATE_GOLDEN=1): %v", golden, err)
	}
	var pinned map[string]string
	if err := json.Unmarshal(raw, &pinned); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range current {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		want, known := pinned[k]
		switch {
		case !known:
			t.Errorf("%s is new: add it to testdata/hashes.json (UPDATE_GOLDEN=1) in the same commit that introduces it", k)
		case want != current[k]:
			t.Errorf("%s CHANGED but a published version is immutable: bump the version (publish %s as a new version) instead of editing it", k, strings.Split(k, "@")[0])
		}
	}
	for k := range pinned {
		if _, still := current[k]; !still {
			t.Errorf("%s was removed: published template/pack versions are never deleted", k)
		}
	}
}

func TestPacksResolveAndAggregateMappingsOnce(t *testing.T) {
	r := registry(t)
	for _, p := range r.Packs() {
		all, err := r.Closure(p, nil)
		if err != nil {
			t.Fatalf("pack %s: %v", p.Slug, err)
		}
		// dependencies always come BEFORE the templates that call them
		pos := map[string]int{}
		for i, tpl := range all {
			pos[tpl.Slug] = i
		}
		for _, tpl := range all {
			for _, dep := range tpl.Requires {
				if pos[dep] >= pos[tpl.Slug] {
					t.Errorf("pack %s: %s is installed before its dependency %s", p.Slug, tpl.Slug, dep)
				}
			}
		}
		seen := map[string]bool{}
		for _, m := range templates.Mappings(all) {
			if seen[m.Key] {
				t.Errorf("pack %s: mapping %s asked more than once", p.Slug, m.Key)
			}
			seen[m.Key] = true
		}
		if len(p.Items) == 0 || p.Name == "" || len(p.RecommendedFor) == 0 {
			t.Errorf("pack %s: incomplete", p.Slug)
		}
	}
}

// ---- Given/When/Then runner ------------------------------------------------------------------------------------

func toApp(sc templates.Scenario) application.Scenario {
	out := application.Scenario{Contact: application.SimContact{Name: sc.ContactName, Kind: sc.ContactKind}, Provider: sc.Provider, WindowOpen: sc.WindowOpen, OpenTickets: sc.OpenTickets, OpenTicketSubject: "Link fora do ar", Now: sc.Now}
	for _, c := range sc.Companies {
		out.Companies = append(out.Companies, application.SimCompany{Name: c})
	}
	for _, e := range sc.Events {
		if e.Timeout {
			out.Events = append(out.Events, application.SimEvent{Type: "timeout"})
		} else {
			out.Events = append(out.Events, application.SimEvent{Type: "message", Text: e.Text})
		}
	}
	return out
}

// simInput builds the simulation of a template together with every subflow it (transitively) calls, pinned like a publish would.
func simInput(t *testing.T, r *templates.Registry, tpl *templates.Template, sc templates.Scenario) application.SimInput {
	t.Helper()
	versions := map[uuid.UUID]*domain.FlowVersion{}
	var pinsFor func(tp *templates.Template) map[string]uuid.UUID
	pinsFor = func(tp *templates.Template) map[string]uuid.UUID {
		pins := map[string]uuid.UUID{}
		for _, dep := range tp.Requires {
			d, ok := r.Template(dep, 0)
			if !ok {
				t.Fatalf("missing dependency %s", dep)
			}
			resolved, err := templates.Resolve(d.Definition, syntheticMapping(d), nil)
			if err != nil {
				t.Fatal(err)
			}
			id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("sub:"+d.Slug))
			versions[id] = &domain.FlowVersion{ID: id, Definition: resolved, SubflowPins: pinsFor(d)}
			pins[dep] = id
		}
		return pins
	}
	resolved, err := templates.Resolve(tpl.Definition, syntheticMapping(tpl), nil)
	if err != nil {
		t.Fatal(err)
	}
	app := toApp(sc)
	if sc.ContactName == "" {
		app.Contact.Name = "Contato Teste"
	}
	return application.SimInput{Raw: resolved, Pins: pinsFor(tpl), Versions: versions, Scenario: app}
}

func tenantCtx() context.Context {
	tc, _ := tenancydomain.NewTenantContext(uuid.New(), uuid.New(), tenancydomain.AccessSourceDirect)
	return tenancydomain.WithTenantContext(context.Background(), tc)
}

func check(t *testing.T, label string, res *application.SimResult, e templates.Expect) {
	t.Helper()
	fail := func(format string, a ...any) {
		t.Errorf("%s: %s\n  status=%s waiting=%s error=%q\n  path=%v\n  messages=%v\n  effects=%v vars=%v", label, fmt.Sprintf(format, a...),
			res.Status, res.Waiting, res.Error, stepIDs(res), msgTexts(res), effectKinds(res), res.Variables)
	}
	if e.Status != "" && res.Status != e.Status {
		fail("status %q, want %q", res.Status, e.Status)
	}
	for _, n := range e.Reaches {
		if !res.Reached(n) {
			fail("node %q was not reached", n)
		}
	}
	for _, n := range e.NotReaches {
		if res.Reached(n) {
			fail("node %q must not be reached", n)
		}
	}
	joined := strings.Join(msgTexts(res), "\n")
	pos := 0
	for _, s := range e.Say {
		i := strings.Index(joined[pos:], s)
		if i < 0 {
			fail("messages do not contain %q (in order)", s)
			break
		}
		pos += i + len(s)
	}
	if e.NoMessages && len(res.Messages) != 0 {
		fail("no message expected")
	}
	if e.NoEffects && len(effectKinds(res)) != 0 {
		fail("no effect expected")
	}
	if len(e.Effects) > 0 && strings.Join(effectKinds(res), ",") != strings.Join(e.Effects, ",") {
		fail("effects %v, want %v", effectKinds(res), e.Effects)
	}
	if e.Priority != "" {
		got := ""
		for _, ef := range res.Effects {
			if ef.Kind == "ticket" {
				got = fmt.Sprint(ef.Detail["priority"])
				break
			}
		}
		if got != e.Priority {
			fail("ticket priority %q, want %q", got, e.Priority)
		}
	}
	for k, v := range e.Vars {
		if fmt.Sprint(res.Variables[k]) != v {
			fail("variable %s = %v, want %q", k, res.Variables[k], v)
		}
	}
	if e.WaitingAt != "" && res.Waiting != e.WaitingAt {
		fail("waiting at %q, want %q", res.Waiting, e.WaitingAt)
	}
}

func stepIDs(r *application.SimResult) []string {
	var out []string
	for _, s := range r.Steps {
		out = append(out, s.NodeID)
	}
	return out
}
func msgTexts(r *application.SimResult) []string {
	var out []string
	for _, m := range r.Messages {
		out = append(out, m.Text)
	}
	return out
}

// effectKinds lists what the flow itself decided. return_to_queue is the engine's safety net (a flow that ends without a
// handoff returns the conversation to the normal queue flow), not a template decision, so expectations do not list it.
func effectKinds(r *application.SimResult) []string {
	var out []string
	for _, e := range r.Effects {
		if e.Kind != "return_to_queue" {
			out = append(out, e.Kind)
		}
	}
	return out
}

// Every template ships Given/When/Then cases; they run through the REAL simulator (the same engine as production).
func TestEveryTemplatePassesItsOwnScenarios(t *testing.T) {
	r := registry(t)
	n := 0
	for _, tpl := range r.AllVersions() {
		for _, c := range tpl.Tests {
			n++
			res, err := application.NewSimulator(nil).Simulate(tenantCtx(), simInput(t, r, tpl, c.Scenario))
			if err != nil {
				t.Errorf("%s / %s: %v", tpl.Slug, c.Name, err)
				continue
			}
			if res.Status == "blocked" {
				t.Errorf("%s / %s: the template is blocked by validation: %+v", tpl.Slug, c.Name, res.Issues)
				continue
			}
			check(t, tpl.Slug+" / "+c.Name, res, c.Expect)
		}
	}
	t.Logf("%d template scenarios executed", n)
}
