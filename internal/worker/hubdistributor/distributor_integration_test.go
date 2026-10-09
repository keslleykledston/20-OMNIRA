package hubdistributor_test

// The worker loop on a real PostgreSQL: one RunOnce hands every open, unassigned conversation of a round-robin pool to a member who
// may answer it; a manual pool, a closed conversation and a suspended company are left alone.

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
	"github.com/omnira/omnira/internal/worker/hubdistributor"
)

func TestRunOnce_DistributesOnlyWhatARoundRobinPoolMayTake(t *testing.T) {
	ownerURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var roleAgent uuid.UUID
	if err := owner.QueryRow(ctx, `SELECT id FROM roles WHERE tenant_id IS NULL AND key = 'hub_agent'`).Scan(&roleAgent); err != nil {
		t.Fatal(err)
	}
	hub, agent := uuid.New(), uuid.New()
	exec(`INSERT INTO service_hubs (id, name) VALUES ($1, $2)`, hub, "Hub "+hub.String()[:8])
	exec(`INSERT INTO users (id, external_subject) VALUES ($1, $2)`, agent, "agent-"+agent.String())
	exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, hub, agent, roleAgent)
	tenant := map[string]uuid.UUID{}
	for _, k := range []string{"auto", "manual", "suspended"} {
		id, contract := uuid.New(), uuid.New()
		tenant[k] = id
		exec(`INSERT INTO tenants (id, legal_name, status) VALUES ($1, $2, 'active')`, id, "T "+k+" "+id.String()[:8])
		exec(`INSERT INTO hub_tenant_service_contracts (id, hub_id, tenant_id, valid_from) VALUES ($1, $2, $3, now() - interval '1 day')`, contract, hub, id)
		exec(`INSERT INTO effective_access_grants (hub_id, user_id, tenant_id, service_contract_id, can_reply, valid_from) VALUES ($1, $2, $3, $4, true, now() - interval '1 day')`, hub, agent, id, contract)
		mode := "round_robin"
		if k == "manual" {
			mode = "manual"
		}
		var pool uuid.UUID
		if err := owner.QueryRow(ctx, `INSERT INTO work_pools (hub_id, name, distribution) VALUES ($1, $2, $3) RETURNING id`, hub, "P "+k, mode).Scan(&pool); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO work_pool_members (work_pool_id, hub_id, user_id) VALUES ($1, $2, $3)`, pool, hub, agent)
		exec(`INSERT INTO work_pool_instances (work_pool_id, hub_id, tenant_id) VALUES ($1, $2, $3)`, pool, hub, id)
	}
	conv := func(k string) uuid.UUID {
		contact, c := uuid.New(), uuid.New()
		exec(`INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES ($1, $2, 'C', $3)`, contact, tenant[k], fmt.Sprintf("+5511%09d", rand.Intn(1_000_000_000)))
		exec(`INSERT INTO conversations (id, tenant_id, contact_id) VALUES ($1, $2, $3)`, c, tenant[k], contact)
		exec(`INSERT INTO messages (tenant_id, conversation_id, direction) VALUES ($1, $2, 'inbound')`, tenant[k], c)
		exec(`INSERT INTO hub_inbox_items (hub_id, tenant_id, conversation_id) VALUES ($1, $2, $3)`, hub, tenant[k], c)
		return c
	}
	cAuto, cManual, cSusp := conv("auto"), conv("manual"), conv("suspended")
	cClosed := conv("auto")
	exec(`UPDATE conversations SET status = 'closed' WHERE id = $1`, cClosed)
	exec(`UPDATE hub_inbox_items SET status = 'closed' WHERE conversation_id = $1`, cClosed)
	exec(`UPDATE tenants SET status = 'suspended' WHERE id = $1`, tenant["suspended"])

	tried, assigned, err := hubdistributor.New(app).RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	holder := func(c uuid.UUID) *uuid.UUID {
		var u *uuid.UUID
		if err := owner.QueryRow(ctx, `SELECT assigned_to_user_id FROM conversations WHERE id = $1`, c).Scan(&u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	if h := holder(cAuto); h == nil || *h != agent {
		t.Fatal("the conversation of the round-robin pool must be assigned")
	}
	for name, c := range map[string]uuid.UUID{"manual pool": cManual, "suspended company": cSusp, "closed": cClosed} {
		if holder(c) != nil {
			t.Errorf("%s must be left alone", name)
		}
	}
	if assigned < 1 || tried < 1 {
		t.Errorf("tried=%d assigned=%d", tried, assigned)
	}
	// running again changes nothing (idempotent)
	if _, again, err := hubdistributor.New(app).RunOnce(ctx); err != nil || again != 0 {
		t.Errorf("a second round must assign nothing: %d %v", again, err)
	}
}
