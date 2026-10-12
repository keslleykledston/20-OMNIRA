package httpserver

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
)

// ADR-0018 addendum: the per-conversation event stream refuses (as for an unknown id) the conversation of a contact declared internal to
// whoever lacks conversation.read_internal, and keeps serving the ordinary conversations.
func TestConversationStreamHidesInternalContactConversationsFromAgents(t *testing.T) {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := seed.Exec(ctx, q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	tenant := uuid.New()
	exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenant, tenant.String())
	member := func(roleKey string) uuid.UUID {
		u := uuid.New()
		exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u.String(), u.String()+"@invalid")
		var role uuid.UUID
		if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, roleKey).Scan(&role); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenant, u, role)
		return u
	}
	agent, supervisor := member("tenant_agent"), member("tenant_supervisor")
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = seed.Exec(bg, `DELETE FROM tenants WHERE id=$1`, tenant)
		_, _ = seed.Exec(bg, `DELETE FROM users WHERE id IN ($1,$2)`, agent, supervisor)
	})
	phones := 0
	conversation := func(kind string) uuid.UUID {
		contact, conv := uuid.New(), uuid.New()
		phones++
		exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'C',$3)`, contact, tenant, fmt.Sprintf("+559295550%03d", phones))
		if kind == "internal" {
			exec(`UPDATE contacts SET kind='internal', internal_role='team' WHERE id=$1`, contact)
		}
		exec(`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, conv, tenant, contact)
		return conv
	}
	plain, internal := conversation("other"), conversation("internal")

	auth := sessionStreamAuthorizer{pool: app}
	for _, c := range []struct {
		name string
		user uuid.UUID
		conv uuid.UUID
		want bool
	}{
		{"agent, ordinary conversation", agent, plain, true},
		{"agent, internal contact", agent, internal, false},
		{"supervisor, internal contact", supervisor, internal, true},
		{"supervisor, ordinary conversation", supervisor, plain, true},
		{"agent, unknown id", agent, uuid.New(), false},
	} {
		got, err := auth.ConversationVisible(ctx, c.user, tenant, c.conv)
		if err != nil || got != c.want {
			t.Errorf("%s: visible=%v err=%v, want %v", c.name, got, err, c.want)
		}
	}
}
