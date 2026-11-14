package adapters_test

// ADR-0059 on a real PostgreSQL: the Hub group organizer catalog/links/relationships tables (migration
// 183) reuse the delegated-management mechanism of ADR-0038/migration 103 (management_scopes 'groups' +
// effective_access_grants.can_manage + has_hub_manage_access()) instead of a new permission family.
// Nothing here ever touches wa_groups/wa_group_messages: these are brand-new, Hub-scoped tables.
//
// Run with: scripts/test-integration-one.sh ./internal/hub/adapters TestHubGroupOrganizerRLS

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/google/uuid"
)

// channelConnection inserts a minimal active WhatsApp connection for a tenant so hub_group_catalog's
// composite FK (source_tenant_id, source_channel_connection_id) -> channel_connections(tenant_id, id)
// has something real to point at, exactly like wa_groups already requires.
func (w *world) channelConnection(tenantKey string) uuid.UUID {
	id := uuid.New()
	w.exec(`INSERT INTO channel_connections (id, tenant_id, channel, provider, provider_kind, external_number_id, status)
	        VALUES ($1, $2, 'whatsapp', 'waha', 'unofficial', $3, 'active')`,
		id, w.tenant[tenantKey], fmt.Sprintf("5511%010d", rand.Intn(1_000_000_000)))
	return id
}

func (w *world) catalogEntry(hub, sourceTenantKey string, conn uuid.UUID, providerGroupID, name string) uuid.UUID {
	_ = hub // reserved for a future multi-hub fixture; today world has exactly one hub
	id := uuid.New()
	w.exec(`INSERT INTO hub_group_catalog (id, hub_id, source_tenant_id, source_channel_connection_id, provider_group_id, display_name)
	        VALUES ($1, $2, $3, $4, $5, $6)`,
		id, w.hub, w.tenant[sourceTenantKey], conn, providerGroupID, name)
	return id
}

// waGroup inserts a tenant-owned wa_groups row directly (bypassing RLS, like channelConnection above) so
// hub_discover_tenant_groups (migration 000187) has something real to read. enabled is set so a test can
// prove the discovery bridge never echoes it back.
func (w *world) waGroup(tenantKey string, conn uuid.UUID, providerGroupID, name string, enabled bool) uuid.UUID {
	id := uuid.New()
	w.exec(`INSERT INTO wa_groups (id, tenant_id, channel_connection_id, provider_group_id, name, enabled)
	        VALUES ($1, $2, $3, $4, $5, $6)`,
		id, w.tenant[tenantKey], conn, providerGroupID, name, enabled)
	return id
}

// groupsScope delegates the 'groups' management scope on the given tenant's contract with this hub.
// (world.canManage, used below, already exists in manage_integration_test.go.)
func (w *world) groupsScope(tenantKey string) {
	w.exec(`UPDATE hub_tenant_service_contracts SET management_scopes = array_append(management_scopes, 'groups')
	        WHERE id = $1 AND NOT ('groups' = ANY(management_scopes))`, w.contract[tenantKey])
}

func TestHubGroupOrganizerRLS_CatalogVisibleOnlyWithGroupsScopeOnOrigin(t *testing.T) {
	w := newWorld(t)
	// management_scopes lives on the (hub, tenant) CONTRACT, shared by every grant on that tenant, so
	// "contract never delegated 'groups'" must be tested on a DIFFERENT tenant than "delegated but
	// this grant lacks can_manage" — both cannot coexist on the same contract at once.
	connA := w.channelConnection("A")
	w.catalogEntry("", "A", connA, "111110000000001@g.us", "Alpha NOC")
	connC := w.channelConnection("C")
	w.catalogEntry("", "C", connC, "111110000000011@g.us", "Gamma NOC")

	alice := w.hubAgent("alice")
	w.canManage(w.grant(alice, "A"))
	w.groupsScope("A") // A's contract: 'groups' delegated

	bob := w.hubAgent("bob") // can_manage on C, but C's CONTRACT never delegates 'groups'
	w.canManage(w.grant(bob, "C"))

	carol := w.hubAgent("carol") // A's contract already has 'groups' (from alice's setup above), but carol's own grant lacks can_manage
	w.grant(carol, "A")

	dave := w.hubAgent("dave") // groups+can_manage, but only on tenant B, not A
	w.canManage(w.grant(dave, "B"))
	w.groupsScope("B")

	if got := w.n(alice, `SELECT count(*) FROM hub_group_catalog WHERE source_tenant_id = $1`, w.tenant["A"]); got != 1 {
		t.Fatalf("alice (groups+can_manage on A): got %d catalog rows, want 1", got)
	}
	if got := w.n(bob, `SELECT count(*) FROM hub_group_catalog WHERE source_tenant_id = $1`, w.tenant["C"]); got != 0 {
		t.Fatalf("bob (can_manage but C's contract never delegated 'groups'): got %d, want 0", got)
	}
	if got := w.n(carol, `SELECT count(*) FROM hub_group_catalog WHERE source_tenant_id = $1`, w.tenant["A"]); got != 0 {
		t.Fatalf("carol ('groups' delegated on A but grant lacks can_manage): got %d, want 0", got)
	}
	if got := w.n(dave, `SELECT count(*) FROM hub_group_catalog WHERE source_tenant_id = $1`, w.tenant["A"]); got != 0 {
		t.Fatalf("dave (groups+can_manage only on B): got %d, want 0", got)
	}
}

func TestHubGroupOrganizerRLS_DirectTenantMembershipAloneGrantsNothing(t *testing.T) {
	w := newWorld(t)
	connA := w.channelConnection("A")
	w.catalogEntry("", "A", connA, "111110000000002@g.us", "Alpha Campo")

	// erin is a REAL direct member of tenant A (the group.read/group.manage path), with no hub
	// membership at all. The Hub group organizer must not be reachable through that door.
	erin := w.user("erin")
	w.directMember(erin, "A")

	if got := w.n(erin, `SELECT count(*) FROM hub_group_catalog WHERE source_tenant_id = $1`, w.tenant["A"]); got != 0 {
		t.Fatalf("direct tenant membership alone: got %d catalog rows, want 0 (SEC-011)", got)
	}
}

func TestHubGroupOrganizerRLS_LinkVisibilityIsPerTenantNotPerGroup(t *testing.T) {
	w := newWorld(t)
	connA := w.channelConnection("A")
	group := w.catalogEntry("", "A", connA, "111110000000003@g.us", "Alpha x Beta - Transito IP")

	w.groupsScope("A")
	w.groupsScope("B")
	owner := w.hubAgent("owner") // sets up links using full access to both tenants
	w.canManage(w.grant(owner, "A"))
	w.canManage(w.grant(owner, "B"))
	if _, err := w.write(owner,
		`INSERT INTO hub_group_tenant_links (id, hub_id, hub_group_id, tenant_id, role) VALUES ($1,$2,$3,$4,'consumer')`,
		uuid.New(), w.hub, group, w.tenant["A"]); err != nil {
		t.Fatalf("owner linking A (authorized both sides): %v", err)
	}
	if _, err := w.write(owner,
		`INSERT INTO hub_group_tenant_links (id, hub_id, hub_group_id, tenant_id, role) VALUES ($1,$2,$3,$4,'supplier')`,
		uuid.New(), w.hub, group, w.tenant["B"]); err != nil {
		t.Fatalf("owner linking B (authorized both sides): %v", err)
	}

	aliceA := w.hubAgent("aliceA") // only authorized for A
	w.canManage(w.grant(aliceA, "A"))

	if got := w.n(aliceA, `SELECT count(*) FROM hub_group_tenant_links WHERE hub_group_id = $1 AND tenant_id = $2`, group, w.tenant["A"]); got != 1 {
		t.Fatalf("aliceA sees her own tenant's link: got %d, want 1", got)
	}
	if got := w.n(aliceA, `SELECT count(*) FROM hub_group_tenant_links WHERE hub_group_id = $1 AND tenant_id = $2`, group, w.tenant["B"]); got != 0 {
		t.Fatalf("aliceA must NOT see the same group's link to B (RF-13/SEC-013): got %d, want 0", got)
	}
	if got := w.n(owner, `SELECT count(*) FROM hub_group_tenant_links WHERE hub_group_id = $1`, group); got != 2 {
		t.Fatalf("owner (authorized both sides) sees both links: got %d, want 2", got)
	}
}

func TestHubGroupOrganizerRLS_LinkInsertRequiresDestinationScope(t *testing.T) {
	w := newWorld(t)
	connA := w.channelConnection("A")
	group := w.catalogEntry("", "A", connA, "111110000000004@g.us", "Alpha Suporte")
	w.groupsScope("A")
	// B deliberately never gets the 'groups' scope delegated in this test.

	aliceA := w.hubAgent("aliceA")
	w.canManage(w.grant(aliceA, "A"))
	w.canManage(w.grant(aliceA, "B")) // can_manage on B's OTHER scopes, but contract never delegates 'groups' for B

	affected, err := w.write(aliceA,
		`INSERT INTO hub_group_tenant_links (id, hub_id, hub_group_id, tenant_id, role) VALUES ($1,$2,$3,$4,'supplier')`,
		uuid.New(), w.hub, group, w.tenant["B"])
	if err == nil && affected > 0 {
		t.Fatal("linking to B without 'groups' delegated on B's contract must be refused (SEC-005), but it succeeded")
	}
}

func TestHubGroupOrganizerRLS_RelationshipRequiresBothSides(t *testing.T) {
	w := newWorld(t)
	w.groupsScope("A")
	w.groupsScope("B")

	owner := w.hubAgent("owner")
	w.canManage(w.grant(owner, "A"))
	w.canManage(w.grant(owner, "B"))
	if _, err := w.write(owner,
		`INSERT INTO hub_tenant_relationships (id, hub_id, consumer_tenant_id, supplier_tenant_id, service_kind) VALUES ($1,$2,$3,$4,'transito_ip')`,
		uuid.New(), w.hub, w.tenant["A"], w.tenant["B"]); err != nil {
		t.Fatalf("owner creating A->B relationship: %v", err)
	}

	aliceA := w.hubAgent("aliceA") // only A
	w.canManage(w.grant(aliceA, "A"))

	if got := w.n(owner, `SELECT count(*) FROM hub_tenant_relationships WHERE consumer_tenant_id = $1`, w.tenant["A"]); got != 1 {
		t.Fatalf("owner (both sides authorized) sees the relationship: got %d, want 1", got)
	}
	if got := w.n(aliceA, `SELECT count(*) FROM hub_tenant_relationships WHERE consumer_tenant_id = $1`, w.tenant["A"]); got != 0 {
		t.Fatalf("aliceA (only A authorized) must not see the relationship with B at all (SEC-002): got %d, want 0", got)
	}
}

func TestHubGroupOrganizerRLS_RevocationFailsClosed(t *testing.T) {
	w := newWorld(t)
	connA := w.channelConnection("A")
	w.catalogEntry("", "A", connA, "111110000000005@g.us", "Alpha NOC 2")
	w.groupsScope("A")

	alice := w.hubAgent("alice")
	grantID := w.grant(alice, "A")
	w.canManage(grantID)

	if got := w.n(alice, `SELECT count(*) FROM hub_group_catalog WHERE source_tenant_id = $1`, w.tenant["A"]); got != 1 {
		t.Fatalf("before revocation: got %d, want 1", got)
	}

	w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, grantID)

	if got := w.n(alice, `SELECT count(*) FROM hub_group_catalog WHERE source_tenant_id = $1`, w.tenant["A"]); got != 0 {
		t.Fatalf("after grant revocation: got %d, want 0 (fail closed, no caching)", got)
	}
}

func TestHubGroupOrganizerRLS_CrossHubLinkIsRejectedByForeignKey(t *testing.T) {
	w := newWorld(t)
	connA := w.channelConnection("A")
	group := w.catalogEntry("", "A", connA, "111110000000006@g.us", "Alpha Comercial")
	w.groupsScope("A")

	otherHub := uuid.New()
	w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, $2)`, otherHub, "Other Hub "+otherHub.String()[:8])
	w.exec(`INSERT INTO hub_tenant_service_contracts (id, hub_id, tenant_id, valid_from, management_scopes)
	        VALUES ($1, $2, $3, now() - interval '1 day', ARRAY['groups'])`, uuid.New(), otherHub, w.tenant["A"])

	owner := w.hubAgent("owner")
	w.canManage(w.grant(owner, "A"))

	// group belongs to w.hub, not otherHub: the composite FK (hub_group_id, hub_id) must reject this.
	if _, err := w.write(owner,
		`INSERT INTO hub_group_tenant_links (id, hub_id, hub_group_id, tenant_id, role) VALUES ($1,$2,$3,$4,'operator')`,
		uuid.New(), otherHub, group, w.tenant["A"]); err == nil {
		t.Fatal("linking a group to a hub it was not cataloged under must fail the composite FK (SEC-014), but it succeeded")
	}
}
