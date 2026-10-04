package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestTheRegistryIsClosedAndHasNoDangerousTools(t *testing.T) {
	reg := ToolRegistry()
	if len(reg) != 5 {
		t.Fatalf("registry has %d tools", len(reg))
	}
	// capabilities that must NEVER be reachable by the AI, however they are spelled
	for _, name := range []string{"message.send", "ticket.close", "ticket.update_status", "crm.update", "script.run", "sql.query", "http.request", "tenant.delete", "topic.merge", "topic.delete", "handoff.create"} {
		if _, ok := reg[name]; ok {
			t.Errorf("%s must not be a gateway tool", name)
		}
	}
	for name, s := range reg {
		if s.Name != name || s.Args == nil || s.Permission == "" || (s.Risk != RiskRead && s.Risk != RiskLowWrite) {
			t.Errorf("%s is incomplete: %+v", name, s)
		}
		if s.Risk == RiskLowWrite && s.Permission != "topic.manage" {
			t.Errorf("a write tool must need topic.manage: %s", name)
		}
	}
}

func TestDecideToolNeverLetsTheAIWriteOnItsOwn(t *testing.T) {
	reg := ToolRegistry()
	for name, s := range reg {
		got := DecideTool(s, SourceAIToolCall)
		if s.Risk == RiskRead && got != DecisionExecute {
			t.Errorf("%s (read by AI) = %s", name, got)
		}
		if s.Risk == RiskLowWrite && got != DecisionNeedsApproval {
			t.Errorf("%s (write by AI) = %s, must wait for a person", name, got)
		}
		if DecideTool(s, SourceAgentTool) != DecisionExecute {
			t.Errorf("%s by a person is the person acting", name)
		}
	}
}

func TestToolArgumentsAreStrict(t *testing.T) {
	reg := ToolRegistry()
	read := reg["topic.get_summary"]
	for _, bad := range []string{`{"tenant_id":"x"}`, `{"url":"http://evil"}`, `{"sql":"drop table x"}`, `[]`, `"x"`, `{} {}`, `{"a":` + strings.Repeat("1", 2000) + `}`, `nope`} {
		if _, err := read.Args([]byte(bad)); !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("%.40q must be rejected: %v", bad, err)
		}
	}
	for _, ok := range []string{``, `{}`, ` {} `} {
		if out, err := read.Args([]byte(ok)); err != nil || string(out) != "{}" {
			t.Errorf("%q -> %s %v", ok, out, err)
		}
	}
	apply := reg["topic.apply_ticket_policy"]
	if out, err := apply.Args([]byte(`{"action":"create"}`)); err != nil || string(out) != `{"action":"create"}` {
		t.Errorf("apply create: %s %v", out, err)
	}
	for _, bad := range []string{`{}`, `{"action":"close_ticket"}`, `{"action":"none"}`, `{"action":"needs_agent"}`, `{"action":"create","ticket_id":"x"}`} {
		if _, err := apply.Args([]byte(bad)); !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("%s must be rejected: %v", bad, err)
		}
	}
}

func TestIdempotencyKeys(t *testing.T) {
	for k, want := range map[string]bool{"abcd1234": true, strings.Repeat("a", 64): true, "short": false, strings.Repeat("a", 65): false, "has space 1": false, "quote\"here1": false, "acentuação1": false, "": false} {
		if ValidIdempotencyKey(k) != want {
			t.Errorf("%q = %v", k, !want)
		}
	}
}
