package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func mustDef(t *testing.T, raw string) *Definition {
	t.Helper()
	d, err := ParseDefinition([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return d
}

func codes(issues []Issue, sev Severity) map[string]int {
	out := map[string]int{}
	for _, i := range issues {
		if i.Severity == sev {
			out[i.Code]++
		}
	}
	return out
}

const validFlow = `{"schema_version":1,
 "variables":[{"name":"topic","type":"string"}],
 "nodes":[
  {"id":"start","type":"trigger"},
  {"id":"hello","type":"send_message","config":{"text":"Hi {{contact.name}}"}},
  {"id":"menu","type":"choice","config":{"text":"How can we help?","variable":"topic","options":[{"id":"tech","label":"Support"},{"id":"fin","label":"Billing"}]}},
  {"id":"q","type":"assign_queue","config":{"queue":"11111111-1111-1111-1111-111111111111"}},
  {"id":"bye","type":"end"},
  {"id":"human","type":"human_handoff","config":{"summary":"topic={{topic}}"}}
 ],
 "edges":[
  {"id":"e1","source":"start","sourcePort":"next","target":"hello"},
  {"id":"e2","source":"hello","sourcePort":"next","target":"menu"},
  {"id":"e3","source":"menu","sourcePort":"tech","target":"q"},
  {"id":"e4","source":"menu","sourcePort":"fin","target":"bye"},
  {"id":"e5","source":"menu","sourcePort":"timeout","target":"bye"},
  {"id":"e6","source":"q","sourcePort":"next","target":"human"}
 ]}`

func TestValidAcceptsAGoodFlow(t *testing.T) {
	issues := Validate(mustDef(t, validFlow), ValidateOptions{})
	if HasErrors(issues) {
		t.Fatalf("valid flow rejected: %+v", issues)
	}
	// The optional choice.other port is unconnected: a warning, not an error.
	if codes(issues, SeverityWarning)["unconnected_optional_port"] == 0 {
		t.Fatalf("expected the optional-port warning: %+v", issues)
	}
	refs := ExtractRefs(mustDef(t, validFlow))
	if len(refs) != 1 || refs[0].Kind != "queue" || refs[0].NodeID != "q" {
		t.Fatalf("refs: %+v", refs)
	}
}

func TestValidateDetectsEachClassOfProblem(t *testing.T) {
	// mutate replaces a fragment of the valid flow and returns the error codes found.
	mutate := func(old, new string) map[string]int {
		if !strings.Contains(validFlow, old) {
			t.Fatalf("fragment %q not in fixture", old)
		}
		d, err := ParseDefinition([]byte(strings.Replace(validFlow, old, new, 1)))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return codes(Validate(d, ValidateOptions{}), SeverityError)
	}
	cases := []struct {
		name, old, new, want string
	}{
		{"missing trigger", `{"id":"start","type":"trigger"},`, ``, "missing_trigger"},
		{"two triggers", `{"id":"bye","type":"end"},`, `{"id":"bye","type":"end"},{"id":"s2","type":"trigger"},`, "multiple_triggers"},
		{"unknown node type", `"type":"end"`, `"type":"teleport"`, "unknown_node_type"},
		{"duplicate node", `{"id":"bye","type":"end"},`, `{"id":"bye","type":"end"},{"id":"bye","type":"end"},`, "duplicate_node"},
		{"dangling edge", `"target":"bye"},
  {"id":"e5"`, `"target":"ghost"},
  {"id":"e5"`, "dangling_edge"},
		{"unknown port", `"sourcePort":"tech"`, `"sourcePort":"nope"`, "invalid_port"},
		{"unconnected required port", `{"id":"e5","source":"menu","sourcePort":"timeout","target":"bye"},`, ``, "unconnected_port"},
		{"port with two destinations", `{"id":"e6"`, `{"id":"e7","source":"menu","sourcePort":"tech","target":"bye"},{"id":"e6"`, "port_already_connected"},
		{"edge into trigger", `{"id":"e6"`, `{"id":"e7","source":"q","sourcePort":"error","target":"start"},{"id":"e6"`, "edge_into_trigger"},
		{"unknown variable", `topic={{topic}}`, `topic={{ghost}}`, "unknown_variable"},
		{"invalid queue id", `11111111-1111-1111-1111-111111111111`, `not-a-uuid`, "invalid_resource"},
		{"unresolved placeholder", `"queue":"11111111-1111-1111-1111-111111111111"`, `"queue":{"$ref":"queue.technical"}`, "placeholder"},
		{"secret key in config", `"summary":"topic={{topic}}"`, `"summary":"x","api_key":"abc"`, "invalid_config"},
		{"empty text", `"text":"Hi {{contact.name}}"`, `"text":"  "`, "missing_text"},
		{"reserved variable", `"variable":"topic"`, `"variable":"contact"`, "reserved_variable"},
	}
	for _, c := range cases {
		got := mutate(c.old, c.new)
		if got[c.want] == 0 {
			t.Errorf("%s: want error %q, got %v", c.name, c.want, got)
		}
	}
}

func TestSecretsAreRejectedEvenInsideAKnownField(t *testing.T) {
	d := mustDef(t, `{"schema_version":1,"nodes":[{"id":"start","type":"trigger"},
	 {"id":"m","type":"send_message","config":{"text":"use Bearer abcdefghijklmnop to log in"}},{"id":"m2","type":"send_message","config":{"text":"-----BEGIN RSA PRIVATE KEY-----"}},{"id":"e","type":"end"}],
	 "edges":[{"id":"1","source":"start","sourcePort":"next","target":"m"},{"id":"2","source":"m","sourcePort":"next","target":"m2"},{"id":"3","source":"m2","sourcePort":"next","target":"e"}]}`)
	// Free text is allowed to contain the word "Bearer"; only an actual key block / AWS key / "Bearer <token>" prefix is rejected.
	got := codes(Validate(d, ValidateOptions{}), SeverityError)
	if got["secret_in_flow"] != 1 {
		t.Fatalf("expected exactly the private key block to be rejected, got %v", got)
	}
	if !LooksLikeSecretKey("password") || !LooksLikeSecretKey("client_secret") || !LooksLikeSecretKey("apiKey") || LooksLikeSecretKey("credential_ref") || LooksLikeSecretKey("token_id") || LooksLikeSecretKey("summary") {
		t.Fatal("secret key heuristics wrong")
	}
}

func TestCyclesAndOrphans(t *testing.T) {
	cyc := mustDef(t, `{"schema_version":1,"nodes":[{"id":"start","type":"trigger"},{"id":"a","type":"set_variable","config":{"assignments":[{"variable":"x","value":"1"}]}},{"id":"b","type":"set_variable","config":{"assignments":[{"variable":"y","value":"1"}]}}],
	 "edges":[{"id":"1","source":"start","sourcePort":"next","target":"a"},{"id":"2","source":"a","sourcePort":"next","target":"b"},{"id":"3","source":"b","sourcePort":"next","target":"a"}]}`)
	if codes(Validate(cyc, ValidateOptions{}), SeverityError)["cycle"] == 0 {
		t.Fatal("a loop must be an error")
	}
	orphan := mustDef(t, strings.Replace(validFlow, `{"id":"bye","type":"end"},`, `{"id":"bye","type":"end"},{"id":"lonely","type":"end"},`, 1))
	issues := Validate(orphan, ValidateOptions{})
	if HasErrors(issues) || codes(issues, SeverityWarning)["unreachable_node"] != 1 {
		t.Fatalf("an orphan must only warn: %+v", issues)
	}
}

func TestPlaceholdersOnlyInTemplates(t *testing.T) {
	src := strings.Replace(validFlow, `"queue":"11111111-1111-1111-1111-111111111111"`, `"queue":{"$ref":"queue.technical"}`, 1)
	if !HasErrors(Validate(mustDef(t, src), ValidateOptions{})) {
		t.Fatal("a tenant flow with a placeholder must not be publishable")
	}
	if HasErrors(Validate(mustDef(t, src), ValidateOptions{AllowPlaceholders: true})) {
		t.Fatal("a template may carry placeholders")
	}
	if len(ExtractRefs(mustDef(t, src))) != 0 {
		t.Fatal("a placeholder is not a tenant resource reference")
	}
}

func TestParseDefinitionLimits(t *testing.T) {
	if _, err := ParseDefinition([]byte(`{"schema_version":2}`)); err == nil {
		t.Fatal("schema_version must be 1")
	}
	if _, err := ParseDefinition([]byte(`{"schema_version":1,"surprise":true}`)); err == nil {
		t.Fatal("unknown top-level fields must be rejected")
	}
	var b strings.Builder
	b.WriteString(`{"schema_version":1,"nodes":[`)
	for i := 0; i <= MaxNodes; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"id":"n` + strings.Repeat("x", 3) + `","type":"end"}`)
	}
	b.WriteString(`]}`)
	if _, err := ParseDefinition([]byte(b.String())); err == nil {
		t.Fatal("too many nodes must be rejected")
	}
	if _, err := ParseDefinition(make([]byte, MaxDefinitionBytes+1)); err == nil {
		t.Fatal("oversized definition must be rejected")
	}
}

func TestEveryNodeTypeHasASpec(t *testing.T) {
	all := []NodeType{NodeTrigger, NodeSendMessage, NodeAsk, NodeChoice, NodeCondition, NodeSwitch, NodeSetVariable, NodeBusinessHours,
		NodeResolveContact, NodeResolveCustomerCtx, NodeCustomerChoice, NodeFindOpenTickets, NodeCreateTicket, NodeAssignQueue, NodeHumanHandoff, NodeSubflow, NodeEnd}
	for _, nt := range all {
		if _, ok := SpecFor(nt); !ok {
			t.Errorf("node type %s has no spec", nt)
		}
	}
	if len(Specs()) != len(all) {
		t.Fatalf("catalog size %d != %d", len(Specs()), len(all))
	}
	// Terminal nodes expose no ports; every non-terminal node must have at least one required port.
	for _, s := range Specs() {
		a := s.Analyze(Node{ID: "n", Type: s.Type})
		if s.Terminal && len(a.Ports) != 0 {
			t.Errorf("%s is terminal but has ports", s.Type)
		}
	}
}

func TestEvalOpIsDeterministic(t *testing.T) {
	tests := []struct {
		op          string
		left, right any
		exists      bool
		want        bool
	}{
		{"eq", "Yes", "yes", true, true},
		{"eq", float64(5), "5", true, true},
		{"neq", "a", "b", true, true},
		{"gt", "10", float64(9), true, true},
		{"gt", "abc", float64(9), true, false}, // not comparable => false, never an error
		{"contains", "Link caiu hoje", "caiu", true, true},
		{"in", "b", []any{"a", "b"}, true, true},
		{"exists", "", nil, true, false},
		{"not_exists", nil, nil, false, true},
		{"eq", nil, "x", false, false},
	}
	for _, c := range tests {
		if got := EvalOp(c.op, c.left, c.right, c.exists); got != c.want {
			t.Errorf("%s(%v,%v,exists=%v)=%v want %v", c.op, c.left, c.right, c.exists, got, c.want)
		}
	}
	vars := map[string]any{"contact": map[string]any{"name": "Ana"}, "n": float64(3)}
	if got := Interpolate("Oi {{contact.name}} #{{ n }} {{ghost}}!", vars); got != "Oi Ana #3 !" {
		t.Fatalf("interpolate: %q", got)
	}
}

func TestBusinessHours(t *testing.T) {
	c := BusinessHoursConfig{Timezone: "America/Sao_Paulo", Windows: []HoursWindow{{Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "09:00", End: "18:00"}}}
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	// 2026-10-05 is a Monday.
	open, err := IsOpen(c, time.Date(2026, 10, 5, 10, 0, 0, 0, loc))
	if err != nil || !open {
		t.Fatalf("Monday 10:00 must be open: %v %v", open, err)
	}
	for _, at := range []time.Time{time.Date(2026, 10, 5, 18, 0, 0, 0, loc), time.Date(2026, 10, 5, 8, 59, 0, 0, loc), time.Date(2026, 10, 4, 10, 0, 0, 0, loc)} {
		if open, _ := IsOpen(c, at); open {
			t.Fatalf("%v must be closed", at)
		}
	}
	// The same instant expressed in UTC is judged in the schedule's timezone.
	if open, _ := IsOpen(c, time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)); !open {
		t.Fatal("13:00 UTC is 10:00 in São Paulo")
	}
	for _, bad := range []BusinessHoursConfig{{Timezone: "Nowhere/City", Windows: c.Windows}, {Timezone: "UTC"}, {Timezone: "UTC", Windows: []HoursWindow{{Days: []string{"funday"}, Start: "09:00", End: "10:00"}}}, {Timezone: "UTC", Windows: []HoursWindow{{Days: []string{"mon"}, Start: "10:00", End: "09:00"}}}} {
		if ValidateBusinessHours(bad) == nil {
			t.Errorf("invalid schedule accepted: %+v", bad)
		}
	}
}

func TestRedact(t *testing.T) {
	in := map[string]any{"text": "hello", "password": "hunter2", "nested": map[string]any{"apiKey": "abc", "ok": "fine"}, "auth": "Bearer abcdefghijkl", "list": []any{"AKIAABCDEFGHIJKLMNOP", "x"}}
	out := Redact(in).(map[string]any)
	if out["password"] != "[redacted]" || out["auth"] != "[redacted]" || out["text"] != "hello" {
		t.Fatalf("redact: %+v", out)
	}
	if out["nested"].(map[string]any)["apiKey"] != "[redacted]" || out["nested"].(map[string]any)["ok"] != "fine" || out["list"].([]any)[0] != "[redacted]" {
		t.Fatalf("redact nested: %+v", out)
	}
	if in["password"] != "hunter2" {
		t.Fatal("Redact must not mutate its input")
	}
	long := Redact(strings.Repeat("a", 5000)).(string)
	if len(long) > 2100 {
		t.Fatalf("long strings must be truncated, got %d", len(long))
	}
	var m map[string]any
	if err := json.Unmarshal(RedactJSON([]byte(`{"token":"x","k":1}`)), &m); err != nil || m["token"] != "[redacted]" {
		t.Fatalf("RedactJSON: %v %v", m, err)
	}
	if string(RedactJSON([]byte(`not json`))) == "not json" {
		t.Fatal("undecodable input must not be stored raw")
	}
	_ = uuid.Nil
}
