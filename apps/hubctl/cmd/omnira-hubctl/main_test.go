package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestParse(t *testing.T) {
	hub, tenant, user, q1, q2 := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ok := []struct {
		name string
		args []string
		chk  func(command) bool
	}{
		{"hub create", []string{"--operator", "ana", "hub", "create", "--name", "K3G"}, func(c command) bool { return c.name == "K3G" && c.operator == "ana" }},
		{"member add defaults to agent", []string{"--operator", "ana", "member", "add", "--hub", hub.String(), "--user", user.String()}, func(c command) bool { return c.role == "hub_agent" && c.user == user }},
		{"member add by e-mail", []string{"--operator", "ana", "member", "add", "--hub", hub.String(), "--email", "a@b.c", "--role", "hub_admin"}, func(c command) bool { return c.email == "a@b.c" && c.role == "hub_admin" }},
		{"contract create with scope and end", []string{"--operator", "ana", "contract", "create", "--hub", hub.String(), "--tenant", tenant.String(), "--queues", q1.String() + "," + q2.String(), "--valid-until", "2030-01-02T03:04:05Z"},
			func(c command) bool { return len(c.queues) == 2 && c.validUntil != nil && c.tenant == tenant }},
		{"grant add read-only by default", []string{"--operator", "ana", "grant", "add", "--hub", hub.String(), "--tenant", tenant.String(), "--user", user.String()}, func(c command) bool { return !c.reply }},
		{"grant add with reply", []string{"--operator", "ana", "grant", "add", "--hub", hub.String(), "--tenant", tenant.String(), "--user", user.String(), "--reply"}, func(c command) bool { return c.reply }},
		{"grant revoke", []string{"--operator", "ana", "grant", "revoke", "--hub", hub.String(), "--tenant", tenant.String(), "--user", user.String()}, func(c command) bool { return c.action == "revoke" }},
		{"show", []string{"--operator", "ana", "show", "--hub", hub.String()}, func(c command) bool { return c.group == "show" && c.hub == hub }},
		{"reconcile needs no hub", []string{"--operator", "ana", "reconcile"}, func(c command) bool { return c.group == "reconcile" && c.hub == uuid.Nil }},
	}
	for _, c := range ok {
		got, err := parse(c.args)
		if err != nil || !c.chk(got) {
			t.Errorf("%s: err=%v cmd=%+v", c.name, err, got)
		}
	}
	bad := map[string][]string{
		"no operator":             {"hub", "create", "--name", "x"},
		"blank operator":          {"--operator", "  ", "hub", "create", "--name", "x"},
		"no command":              {"--operator", "ana"},
		"unknown command":         {"--operator", "ana", "hub", "explode"},
		"unknown group":           {"--operator", "ana", "tenant", "create"},
		"hub create without name": {"--operator", "ana", "hub", "create"},
		"member without hub":      {"--operator", "ana", "member", "add", "--user", user.String()},
		"member without user":     {"--operator", "ana", "member", "add", "--hub", hub.String()},
		"both user and email":     {"--operator", "ana", "member", "add", "--hub", hub.String(), "--user", user.String(), "--email", "a@b.c"},
		"grant without tenant":    {"--operator", "ana", "grant", "add", "--hub", hub.String(), "--user", user.String()},
		"bad uuid":                {"--operator", "ana", "show", "--hub", "not-a-uuid"},
		"nil uuid":                {"--operator", "ana", "show", "--hub", uuid.Nil.String()},
		"bad date":                {"--operator", "ana", "grant", "add", "--hub", hub.String(), "--tenant", tenant.String(), "--user", user.String(), "--valid-until", "tomorrow"},
		"bad queue list":          {"--operator", "ana", "contract", "create", "--hub", hub.String(), "--tenant", tenant.String(), "--queues", "x,y"},
		"status without value":    {"--operator", "ana", "contract", "status", "--hub", hub.String(), "--tenant", tenant.String()},
		"stray argument":          {"--operator", "ana", "show", "--hub", hub.String(), "extra"},
		"unknown flag":            {"--operator", "ana", "show", "--hub", hub.String(), "--force"},
	}
	for name, args := range bad {
		if _, err := parse(args); !errors.Is(err, errUsage) {
			t.Errorf("%s: expected a usage error, got %v", name, err)
		}
	}
	if !strings.Contains(usage, "--operator") {
		t.Error("usage must mention the mandatory operator")
	}
}

func TestAttributedOperator(t *testing.T) {
	if got := attributedOperator("ana", "suporte", "srv1"); got != "ana [os suporte@srv1]" {
		t.Errorf("got %q", got)
	}
	if got := attributedOperator("ana", "", " "); got != "ana [os ?@?]" {
		t.Errorf("missing identity must be visible, got %q", got)
	}
	if got := attributedOperator("ana", "su\x00po\nrte", "srv1"); strings.ContainsAny(got, "\x00\n") {
		t.Errorf("control characters must be stripped, got %q", got)
	}
	long := strings.Repeat("x", 300)
	got := attributedOperator(long, "suporte", "srv1")
	if n := len([]rune(got)); n > 100 {
		t.Errorf("exceeds the audit limit: %d", n)
	}
	if !strings.HasSuffix(got, "[os suporte@srv1]") {
		t.Errorf("the OS evidence must survive truncation, got %q", got)
	}
	if n := len([]rune(attributedOperator("a", strings.Repeat("u", 200), strings.Repeat("h", 200)))); n > 100 {
		t.Errorf("an absurd OS identity must still fit: %d", n)
	}
}
