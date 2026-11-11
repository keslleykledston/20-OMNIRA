package adapters_test

// ADR-0059 addendum (autodiscovery): hub_autodiscover_catalog_upsert (migration 000187) is the ONLY way a
// background job -- no human actor, so current_user_id() is NULL and has_hub_manage_access() can never say
// yes -- is allowed to write hub_group_catalog, because that table's RLS (migration 000183) deliberately
// has NO is_system_admin() bypass. This proves, against a real PostgreSQL and through the application role
// (omnira_app) exactly as internal/worker/hubgroupdiscovery calls it:
//   - it refuses (fails closed to zero rows, never an error) when the (hub, tenant) contract never
//     delegated 'groups', or when it is not currently live (expired/not yet valid/revoked);
//   - it is idempotent: calling it twice for the same group updates the SAME row, never duplicates it;
//   - it never overwrites a display_name a human already set manually (the same INSERT the "Adicionar
//     grupo ao catálogo" button performs, internal/hub/grouporganizer/service.go).
//
// Run with: scripts/test-integration-one.sh ./internal/hub/adapters TestHubAutodiscoverCatalogUpsert

import (
	"testing"

	"github.com/google/uuid"
)

// autodiscoverResult is what hub_autodiscover_catalog_upsert returned: found=false means the function's
// own authorization check failed closed (zero rows), not an error.
type autodiscoverResult struct {
	id      uuid.UUID
	created bool
	found   bool
}

// autodiscover calls the SECURITY DEFINER function through the APPLICATION role (w.app), with no tenant
// session at all: the function needs none of current_user_id()/is_system_admin() -- its authorization is
// entirely structural (the live contract row), which is the whole point of this function existing.
func (w *world) autodiscover(tenantKey string, conn uuid.UUID, providerGroupID, displayName string) autodiscoverResult {
	w.t.Helper()
	rows, err := w.app.Query(w.ctx, `SELECT id, created FROM hub_autodiscover_catalog_upsert($1, $2, $3, $4, $5)`,
		w.hub, w.tenant[tenantKey], conn, providerGroupID, displayName)
	w.must(err)
	defer rows.Close()
	var res autodiscoverResult
	for rows.Next() {
		res.found = true
		w.must(rows.Scan(&res.id, &res.created))
	}
	w.must(rows.Err())
	return res
}

func TestHubAutodiscoverCatalogUpsert_RejectsWithoutGroupsScope(t *testing.T) {
	w := newWorld(t)
	conn := w.channelConnection("A")
	// newWorld's own contract for tenant A never delegates 'groups' (w.groupsScope is never called here).

	res := w.autodiscover("A", conn, "222220000000001@g.us", "Nome Autodescoberto")
	if res.found {
		t.Fatalf("contract without 'groups' scope: expected zero rows (fail closed), got created=%v", res.created)
	}
	var n int
	w.must(w.owner.QueryRow(w.ctx, `SELECT count(*) FROM hub_group_catalog WHERE source_tenant_id = $1`, w.tenant["A"]).Scan(&n))
	if n != 0 {
		t.Fatalf("expected no catalog row written, got %d", n)
	}
}

func TestHubAutodiscoverCatalogUpsert_RejectsWhenContractNotLive(t *testing.T) {
	w := newWorld(t)
	conn := w.channelConnection("A")
	w.groupsScope("A")
	w.exec(`UPDATE hub_tenant_service_contracts SET valid_until = now() - interval '1 hour' WHERE id = $1`, w.contract["A"])

	res := w.autodiscover("A", conn, "222220000000002@g.us", "Nome")
	if res.found {
		t.Fatalf("expired contract: expected zero rows (fail closed), got created=%v", res.created)
	}
}

func TestHubAutodiscoverCatalogUpsert_RejectsWhenContractSuspended(t *testing.T) {
	w := newWorld(t)
	conn := w.channelConnection("A")
	w.groupsScope("A")
	w.exec(`UPDATE hub_tenant_service_contracts SET status = 'suspended' WHERE id = $1`, w.contract["A"])

	res := w.autodiscover("A", conn, "222220000000006@g.us", "Nome")
	if res.found {
		t.Fatalf("suspended contract: expected zero rows (fail closed), got created=%v", res.created)
	}
}

func TestHubAutodiscoverCatalogUpsert_CreatesWhenAuthorized(t *testing.T) {
	w := newWorld(t)
	conn := w.channelConnection("A")
	w.groupsScope("A")

	res := w.autodiscover("A", conn, "222220000000003@g.us", "Nome Autodescoberto")
	if !res.found || !res.created {
		t.Fatalf("authorized, first call: expected a created row, got found=%v created=%v", res.found, res.created)
	}
	var name string
	w.must(w.owner.QueryRow(w.ctx, `SELECT display_name FROM hub_group_catalog WHERE id = $1`, res.id).Scan(&name))
	if name != "Nome Autodescoberto" {
		t.Fatalf("display_name = %q, want %q", name, "Nome Autodescoberto")
	}
}

func TestHubAutodiscoverCatalogUpsert_IdempotentAcrossRuns(t *testing.T) {
	w := newWorld(t)
	conn := w.channelConnection("A")
	w.groupsScope("A")

	first := w.autodiscover("A", conn, "222220000000004@g.us", "Nome")
	if !first.found || !first.created {
		t.Fatalf("first call: expected a created row, got found=%v created=%v", first.found, first.created)
	}
	second := w.autodiscover("A", conn, "222220000000004@g.us", "Nome")
	if !second.found || second.created {
		t.Fatalf("second call (same group): expected an UPDATE (created=false), got found=%v created=%v", second.found, second.created)
	}
	if second.id != first.id {
		t.Fatalf("second call touched a different row: %s vs %s", second.id, first.id)
	}
	var n int
	w.must(w.owner.QueryRow(w.ctx, `SELECT count(*) FROM hub_group_catalog WHERE id = $1`, first.id).Scan(&n))
	if n != 1 {
		t.Fatalf("expected exactly 1 row after two idempotent calls, got %d", n)
	}
}

func TestHubAutodiscoverCatalogUpsert_NeverOverwritesManualDisplayName(t *testing.T) {
	w := newWorld(t)
	conn := w.channelConnection("A")
	w.groupsScope("A")
	// A human already registered this group manually with a custom name (the same INSERT the "Adicionar
	// grupo ao catálogo" button performs, internal/hub/grouporganizer/service.go).
	manualID := w.catalogEntry("", "A", conn, "222220000000005@g.us", "Nome Escolhido Pelo Operador")

	res := w.autodiscover("A", conn, "222220000000005@g.us", "Nome Vindo Do WhatsApp")
	if !res.found || res.created {
		t.Fatalf("expected an UPDATE of the existing manual row, got found=%v created=%v", res.found, res.created)
	}
	if res.id != manualID {
		t.Fatalf("autodiscovery matched a different row: %s vs manual %s", res.id, manualID)
	}
	var name string
	w.must(w.owner.QueryRow(w.ctx, `SELECT display_name FROM hub_group_catalog WHERE id = $1`, manualID).Scan(&name))
	if name != "Nome Escolhido Pelo Operador" {
		t.Fatalf("display_name was overwritten: got %q, want the operator's own %q", name, "Nome Escolhido Pelo Operador")
	}
}
