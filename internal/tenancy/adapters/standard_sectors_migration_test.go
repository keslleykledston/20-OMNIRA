package adapters

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
)

// ADR-0043 / migration 000123: every existing company gets Comercial, Financeiro and Suporte (manual, never the default); a company
// that already owns a queue with one of those names keeps it untouched; running it again changes nothing; the down only removes
// what nobody started using. Run inside a transaction that is rolled back, so the shared test database is left as it was.
func TestStandardSectorsMigrationAddsTheThreeSectorsAndRespectsWhatExists(t *testing.T) {
	seed := teamSeedPool(t)
	a, b := seedTeamTenant(t, seed), seedTeamTenant(t, seed)
	member := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	own := seedQueue(t, seed, a.tenantID, "Suporte", "round_robin", true) // A already has its own "Suporte", and it is the default
	if _, err := seed.Exec(context.Background(), `INSERT INTO queue_members (tenant_id, queue_id, user_id, active, available, capacity) VALUES ($1,$2,$3,true,true,3)`, a.tenantID, own, member); err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile("../../../migrations/000123_standard_sectors.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../migrations/000123_standard_sectors.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := seed.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	names := func(tenant uuid.UUID) map[string]string {
		rows, err := tx.Query(ctx, `SELECT name || ':' || mode || ':' || is_default::text, name FROM queues WHERE tenant_id = $1`, tenant)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var full, name string
			if err := rows.Scan(&full, &name); err != nil {
				t.Fatal(err)
			}
			out[name] = full
		}
		return out
	}
	for i := 0; i < 2; i++ { // twice: idempotent
		if _, err := tx.Exec(ctx, string(up)); err != nil {
			t.Fatalf("up run %d: %v", i+1, err)
		}
	}
	gotA, gotB := names(a.tenantID), names(b.tenantID)
	if len(gotA) != 3 || gotA["Suporte"] != "Suporte:round_robin:true" || gotA["Comercial"] != "Comercial:manual:false" || gotA["Financeiro"] != "Financeiro:manual:false" {
		t.Fatalf("company with its own Suporte: %v", gotA)
	}
	if len(gotB) != 3 || gotB["Comercial"] != "Comercial:manual:false" || gotB["Financeiro"] != "Financeiro:manual:false" || gotB["Suporte"] != "Suporte:manual:false" {
		t.Fatalf("company with no queue: %v", gotB)
	}
	var members, defaults int
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM queue_members WHERE queue_id = $1), (SELECT count(*) FROM queues WHERE tenant_id IN ($2,$3) AND is_default)`, own, a.tenantID, b.tenantID).Scan(&members, &defaults); err != nil {
		t.Fatal(err)
	}
	if members != 1 || defaults != 1 {
		t.Fatalf("the company's own queue lost its members or the defaults changed: members=%d defaults=%d", members, defaults)
	}
	// the down removes B's untouched sectors but keeps whatever someone started using
	if _, err := tx.Exec(ctx, `INSERT INTO queue_members (tenant_id, queue_id, user_id, active, available, capacity)
		SELECT $1, q.id, $2, true, true, 1 FROM queues q WHERE q.tenant_id = $1 AND q.name = 'Financeiro'`, a.tenantID, member); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down: %v", err)
	}
	afterA, afterB := names(a.tenantID), names(b.tenantID)
	if len(afterB) != 0 {
		t.Errorf("untouched sectors must be removed by the down: %v", afterB)
	}
	if afterA["Financeiro"] == "" || afterA["Suporte"] == "" || afterA["Comercial"] != "" {
		t.Errorf("the down must keep queues with members and the company's own queue, and remove the untouched one: %v", afterA)
	}
}
