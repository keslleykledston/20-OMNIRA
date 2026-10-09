package routing_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
	workerrouting "github.com/omnira/omnira/internal/worker/routing"
)

// Codex H1 / ADR-0038: the routing worker does not assign the conversations of a suspended company (the job is simply done);
// after the reactivation the same job runs.
func TestRunnerDoesNotRunForASuspendedCompany(t *testing.T) {
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

	tenant, contact, conv := uuid.New(), uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := seed.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenant, "routing suspension")
	t.Cleanup(func() { _, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, tenant) })
	exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'C','+5511999990077')`, contact, tenant)
	exec(`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, conv, tenant, contact)

	runner := workerrouting.NewPostgresConversationRunner(app)
	ran := 0
	run := func() {
		t.Helper()
		if err := runner.RunForConversation(ctx, conv, func(context.Context) error { ran++; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	run()
	if ran != 1 {
		t.Fatalf("an active company's job must run, ran=%d", ran)
	}
	exec(`UPDATE tenants SET status='suspended' WHERE id=$1`, tenant)
	run()
	if ran != 1 {
		t.Fatalf("a suspended company's job ran, ran=%d", ran)
	}
	exec(`UPDATE tenants SET status='active' WHERE id=$1`, tenant)
	run()
	if ran != 2 {
		t.Fatalf("after the reactivation the job must run, ran=%d", ran)
	}
}
