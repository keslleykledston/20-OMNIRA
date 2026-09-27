package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/crmevidence/ports"
	"github.com/omnira/omnira/internal/testhelpers"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// PRODUCT.7B2B real-Postgres proof for the durable Contact<->Company
// evidence foundation: the partial unique index / idempotent upsert, FK
// tenant-safety, and forced RLS behavior this design depends on.

func evidenceContext(ctx context.Context, tenantID, userID uuid.UUID) (context.Context, error) {
	tc, err := tenancydomain.NewTenantContext(tenantID, userID, tenancydomain.AccessSourceDirect)
	if err != nil {
		return nil, err
	}
	return tenancydomain.WithTenantContext(ctx, tc), nil
}

type evidenceFixture struct {
	seed, app            *pgxpool.Pool
	tenantA, tenantB     uuid.UUID
	userA, userB         uuid.UUID
	contactA, contactB   uuid.UUID
	connA, connB, connA2 uuid.UUID
	// convA/convB, ticketA/ticketB: real rows, used as origin_conversation_id/
	// origin_ticket_id — the FK to tickets(tenant_id,id)/conversations(tenant_id,id)
	// requires a real row (or NULL), never an arbitrary uuid.New().
	convA, convB     uuid.UUID
	ticketA, ticketB uuid.UUID
	ticketA2         uuid.UUID // a second real ticket, tenant A, for provenance-immutability proofs
}

func setupEvidenceFixture(t *testing.T) *evidenceFixture {
	t.Helper()
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		seed.Close()
		t.Fatal(err)
	}

	f := &evidenceFixture{
		seed: seed, app: app,
		tenantA: uuid.New(), tenantB: uuid.New(),
		userA: uuid.New(), userB: uuid.New(),
		contactA: uuid.New(), contactB: uuid.New(),
		connA: uuid.New(), connB: uuid.New(), connA2: uuid.New(),
		convA: uuid.New(), convB: uuid.New(),
		ticketA: uuid.New(), ticketB: uuid.New(), ticketA2: uuid.New(),
	}

	var roleID uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_admin' AND tenant_id IS NULL LIMIT 1`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	for _, u := range []uuid.UUID{f.userA, f.userB} {
		if _, err := seed.Exec(ctx, `INSERT INTO users(id, external_subject, email, status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid"); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		id   uuid.UUID
		name string
	}{{f.tenantA, "7B2B Tenant A"}, {f.tenantB, "7B2B Tenant B"}} {
		if _, err := seed.Exec(ctx, `INSERT INTO tenants(id, legal_name, status) VALUES($1,$2,'active')`, item.id, item.name); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ tenant, user uuid.UUID }{{f.tenantA, f.userA}, {f.tenantB, f.userB}} {
		if _, err := seed.Exec(ctx, `INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, item.tenant, item.user, roleID); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		id, tenant uuid.UUID
		phone      string
	}{{f.contactA, f.tenantA, "+5511900000001"}, {f.contactB, f.tenantB, "+5511900000002"}} {
		if _, err := seed.Exec(ctx, `INSERT INTO contacts(id, tenant_id, display_name, phone_e164) VALUES($1,$2,'Evidence Test',$3)`,
			item.id, item.tenant, item.phone); err != nil {
			t.Fatal(err)
		}
	}
	for i, item := range []struct {
		id, tenant uuid.UUID
		numberID   string
	}{{f.connA, f.tenantA, "evi-a-1"}, {f.connB, f.tenantB, "evi-b-1"}, {f.connA2, f.tenantA, "evi-a-2"}} {
		if _, err := seed.Exec(ctx, `INSERT INTO channel_connections(id, tenant_id, channel, provider, provider_kind, external_number_id, status)
			VALUES($1,$2,'erp','k3g_crm','official',$3,'active')`, item.id, item.tenant, item.numberID); err != nil {
			t.Fatalf("seed connection %d: %v", i, err)
		}
	}
	// Real conversation/ticket rows: origin_conversation_id/origin_ticket_id
	// carry a composite FK to conversations(tenant_id,id)/tickets(tenant_id,id)
	// — an arbitrary uuid.New() would violate it. NULL would also be
	// accepted by the schema, but every real production caller always has
	// a real conversation/local ticket at this point, so the fixture uses
	// real rows too.
	for _, item := range []struct{ id, tenant, contact uuid.UUID }{{f.convA, f.tenantA, f.contactA}, {f.convB, f.tenantB, f.contactB}} {
		if _, err := seed.Exec(ctx, `INSERT INTO conversations(id, tenant_id, contact_id) VALUES($1,$2,$3)`, item.id, item.tenant, item.contact); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ id, tenant, conversation uuid.UUID }{{f.ticketA, f.tenantA, f.convA}, {f.ticketB, f.tenantB, f.convB}} {
		if _, err := seed.Exec(ctx, `INSERT INTO tickets(id, tenant_id, conversation_id) VALUES($1,$2,$3)`, item.id, item.tenant, item.conversation); err != nil {
			t.Fatal(err)
		}
	}
	// ticketA2 is a SECOND ticket for the same tenant A conversation — must
	// not be 'open' (tickets_active_conversation_uq forbids two open/
	// in_progress/waiting tickets per conversation), it exists purely to
	// give provenance-immutability tests a real, DIFFERENT ticket id.
	if _, err := seed.Exec(ctx, `INSERT INTO tickets(id, tenant_id, conversation_id, status) VALUES($1,$2,$3,'closed')`,
		f.ticketA2, f.tenantA, f.convA); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1,$2)`, f.tenantA, f.tenantB)
		_, _ = seed.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1,$2)`, f.userA, f.userB)
		seed.Close()
		app.Close()
	})

	return f
}

// A/B/C/D: first observation inserts one row; a repeated trusted
// observation of the SAME active fact updates ONLY last_verified_at
// (never first_verified_at, never the FIRST-confirmation provenance).
func TestPostgresEvidenceStoreIdempotentUpsertPreservesFirstProvenance(t *testing.T) {
	f := setupEvidenceFixture(t)
	store := NewPostgresEvidenceStore(f.app)
	origin1, actor1 := f.ticketA, f.userA

	err := platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
		sc, err := evidenceContext(sc, f.tenantA, f.userA)
		if err != nil {
			return err
		}
		return store.RecordTicketSelection(sc, ports.RecordTicketSelectionInput{
			TenantID: f.tenantA, ContactID: f.contactA, ConnectionID: f.connA,
			ExternalCompanyID: "company-A", ActorUserID: actor1, OriginTicketID: origin1, OriginConversationID: f.convA,
		})
	})
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}

	var count int
	var firstVerified, lastVerified time.Time
	var gotActor, gotOrigin uuid.UUID
	readRow := func() {
		if err := platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
			sc, err := evidenceContext(sc, f.tenantA, f.userA)
			if err != nil {
				return err
			}
			return platformdb.QuerierFromContext(sc, f.app).QueryRow(sc, `
				SELECT count(*) OVER (), first_verified_at, last_verified_at, actor_user_id, origin_ticket_id
				FROM crm_contact_company_evidence WHERE contact_id=$1 AND external_company_id='company-A'`, f.contactA,
			).Scan(&count, &firstVerified, &lastVerified, &gotActor, &gotOrigin)
		}); err != nil {
			t.Fatal(err)
		}
	}
	readRow()
	if count != 1 {
		t.Fatalf("after first insert: count = %d, want 1", count)
	}
	if gotActor != actor1 || gotOrigin != origin1 {
		t.Fatalf("provenance = (%s,%s), want (%s,%s)", gotActor, gotOrigin, actor1, origin1)
	}

	time.Sleep(10 * time.Millisecond)
	otherActor, otherOrigin := f.userB, f.ticketA2 // a DIFFERENT actor/origin observes the SAME fact again
	err = platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
		sc, err := evidenceContext(sc, f.tenantA, f.userA)
		if err != nil {
			return err
		}
		return store.RecordTicketSelection(sc, ports.RecordTicketSelectionInput{
			TenantID: f.tenantA, ContactID: f.contactA, ConnectionID: f.connA,
			ExternalCompanyID: "company-A", ActorUserID: otherActor, OriginTicketID: otherOrigin, OriginConversationID: f.convA,
		})
	})
	if err != nil {
		t.Fatalf("repeat observation: %v", err)
	}

	prevFirst, prevActor, prevOrigin := firstVerified, gotActor, gotOrigin
	readRow()
	if count != 1 {
		t.Fatalf("after repeat observation: count = %d, want 1 (no duplicate row)", count)
	}
	if !firstVerified.Equal(prevFirst) {
		t.Fatalf("first_verified_at changed: %v -> %v, must never be rewritten", prevFirst, firstVerified)
	}
	if !lastVerified.After(prevFirst) {
		t.Fatalf("last_verified_at did not advance past the original timestamp")
	}
	if gotActor != prevActor || gotOrigin != prevOrigin {
		t.Fatalf("FIRST provenance was overwritten by a later observation: (%s,%s) -> (%s,%s)", prevActor, prevOrigin, gotActor, gotOrigin)
	}
}

// H: the same contact may have evidence for multiple distinct companies.
func TestPostgresEvidenceStoreSameContactMultipleCompanies(t *testing.T) {
	f := setupEvidenceFixture(t)
	store := NewPostgresEvidenceStore(f.app)
	for _, company := range []string{"company-A", "company-B"} {
		err := platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
			sc, err := evidenceContext(sc, f.tenantA, f.userA)
			if err != nil {
				return err
			}
			return store.RecordTicketSelection(sc, ports.RecordTicketSelectionInput{
				TenantID: f.tenantA, ContactID: f.contactA, ConnectionID: f.connA,
				ExternalCompanyID: company, ActorUserID: f.userA, OriginTicketID: f.ticketA, OriginConversationID: f.convA,
			})
		})
		if err != nil {
			t.Fatalf("insert %s: %v", company, err)
		}
	}
	var n int
	if err := platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
		sc, err := evidenceContext(sc, f.tenantA, f.userA)
		if err != nil {
			return err
		}
		return platformdb.QuerierFromContext(sc, f.app).QueryRow(sc,
			`SELECT count(*) FROM crm_contact_company_evidence WHERE contact_id=$1 AND revoked_at IS NULL`, f.contactA).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("active companies for contact = %d, want 2", n)
	}
}

// I: the same external_company_id under two DIFFERENT connections (same
// tenant) never collides — each connection/workspace is a separate fact.
func TestPostgresEvidenceStoreSameCompanyIDDifferentConnectionsDoNotCollide(t *testing.T) {
	f := setupEvidenceFixture(t)
	store := NewPostgresEvidenceStore(f.app)
	for _, conn := range []uuid.UUID{f.connA, f.connA2} {
		err := platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
			sc, err := evidenceContext(sc, f.tenantA, f.userA)
			if err != nil {
				return err
			}
			return store.RecordTicketSelection(sc, ports.RecordTicketSelectionInput{
				TenantID: f.tenantA, ContactID: f.contactA, ConnectionID: conn,
				ExternalCompanyID: "same-company-id", ActorUserID: f.userA, OriginTicketID: f.ticketA, OriginConversationID: f.convA,
			})
		})
		if err != nil {
			t.Fatalf("insert under connection %s: %v", conn, err)
		}
	}
	var n int
	if err := platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
		sc, err := evidenceContext(sc, f.tenantA, f.userA)
		if err != nil {
			return err
		}
		return platformdb.QuerierFromContext(sc, f.app).QueryRow(sc,
			`SELECT count(*) FROM crm_contact_company_evidence WHERE contact_id=$1 AND external_company_id='same-company-id' AND revoked_at IS NULL`,
			f.contactA).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("rows for the same company id under 2 connections = %d, want 2 (must not collide)", n)
	}
}

// J: the same external_company_id across two different TENANTS never
// collides (RLS + tenant_id in the partial unique index).
func TestPostgresEvidenceStoreSameCompanyIDAcrossTenantsDoNotCollide(t *testing.T) {
	f := setupEvidenceFixture(t)
	store := NewPostgresEvidenceStore(f.app)
	if err := platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
		sc, err := evidenceContext(sc, f.tenantA, f.userA)
		if err != nil {
			return err
		}
		return store.RecordTicketSelection(sc, ports.RecordTicketSelectionInput{
			TenantID: f.tenantA, ContactID: f.contactA, ConnectionID: f.connA,
			ExternalCompanyID: "shared-id", ActorUserID: f.userA, OriginTicketID: f.ticketA, OriginConversationID: f.convA,
		})
	}); err != nil {
		t.Fatalf("tenant A insert: %v", err)
	}
	if err := platformdb.WithTenantSession(context.Background(), f.app, f.userB, false, func(sc context.Context) error {
		sc, err := evidenceContext(sc, f.tenantB, f.userB)
		if err != nil {
			return err
		}
		return store.RecordTicketSelection(sc, ports.RecordTicketSelectionInput{
			TenantID: f.tenantB, ContactID: f.contactB, ConnectionID: f.connB,
			ExternalCompanyID: "shared-id", ActorUserID: f.userB, OriginTicketID: f.ticketB, OriginConversationID: f.convB,
		})
	}); err != nil {
		t.Fatalf("tenant B insert: %v", err)
	}
}

// K/N: tenant isolation + forced RLS. Tenant A's session can never read
// tenant B's evidence rows, even by external_company_id.
func TestPostgresEvidenceStoreTenantIsolation(t *testing.T) {
	f := setupEvidenceFixture(t)
	store := NewPostgresEvidenceStore(f.app)
	if err := platformdb.WithTenantSession(context.Background(), f.app, f.userB, false, func(sc context.Context) error {
		sc, err := evidenceContext(sc, f.tenantB, f.userB)
		if err != nil {
			return err
		}
		return store.RecordTicketSelection(sc, ports.RecordTicketSelectionInput{
			TenantID: f.tenantB, ContactID: f.contactB, ConnectionID: f.connB,
			ExternalCompanyID: "isolated-company", ActorUserID: f.userB, OriginTicketID: f.ticketB, OriginConversationID: f.convB,
		})
	}); err != nil {
		t.Fatalf("tenant B insert: %v", err)
	}
	var n int
	if err := platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
		sc, err := evidenceContext(sc, f.tenantA, f.userA)
		if err != nil {
			return err
		}
		return platformdb.QuerierFromContext(sc, f.app).QueryRow(sc,
			`SELECT count(*) FROM crm_contact_company_evidence WHERE external_company_id='isolated-company'`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("tenant A read %d rows of tenant B's evidence, want 0 (RLS must block this)", n)
	}
}

// L: a cross-tenant contact reference is impossible — the composite FK
// (tenant_id, contact_id) -> contacts(tenant_id, id) rejects it.
func TestPostgresEvidenceStoreCrossTenantContactFKRejected(t *testing.T) {
	f := setupEvidenceFixture(t)
	store := NewPostgresEvidenceStore(f.app)
	err := platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
		sc, err := evidenceContext(sc, f.tenantA, f.userA)
		if err != nil {
			return err
		}
		// tenantA claimed, but contactB belongs to tenantB.
		return store.RecordTicketSelection(sc, ports.RecordTicketSelectionInput{
			TenantID: f.tenantA, ContactID: f.contactB, ConnectionID: f.connA,
			ExternalCompanyID: "x", ActorUserID: f.userA, OriginTicketID: f.ticketA, OriginConversationID: f.convA,
		})
	})
	if err == nil {
		t.Fatal("expected a foreign key violation for a cross-tenant contact reference, got nil error")
	}
}

// M: a cross-tenant connection reference is impossible — the composite FK
// (tenant_id, connection_id) -> channel_connections(tenant_id, id) rejects it.
func TestPostgresEvidenceStoreCrossTenantConnectionFKRejected(t *testing.T) {
	f := setupEvidenceFixture(t)
	store := NewPostgresEvidenceStore(f.app)
	err := platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
		sc, err := evidenceContext(sc, f.tenantA, f.userA)
		if err != nil {
			return err
		}
		// tenantA claimed, but connB belongs to tenantB.
		return store.RecordTicketSelection(sc, ports.RecordTicketSelectionInput{
			TenantID: f.tenantA, ContactID: f.contactA, ConnectionID: f.connB,
			ExternalCompanyID: "x", ActorUserID: f.userA, OriginTicketID: f.ticketA, OriginConversationID: f.convA,
		})
	})
	if err == nil {
		t.Fatal("expected a foreign key violation for a cross-tenant connection reference, got nil error")
	}
}

// F/G: revoking an active fact and then re-observing it inserts a NEW
// active row; the revoked row remains historical, untouched.
func TestPostgresEvidenceStoreRevokeThenReverifyInsertsNewRow(t *testing.T) {
	f := setupEvidenceFixture(t)
	store := NewPostgresEvidenceStore(f.app)
	insert := func() error {
		return platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
			sc, err := evidenceContext(sc, f.tenantA, f.userA)
			if err != nil {
				return err
			}
			return store.RecordTicketSelection(sc, ports.RecordTicketSelectionInput{
				TenantID: f.tenantA, ContactID: f.contactA, ConnectionID: f.connA,
				ExternalCompanyID: "company-revoke-test", ActorUserID: f.userA, OriginTicketID: f.ticketA, OriginConversationID: f.convA,
			})
		})
	}
	if err := insert(); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// Revocation has no application-layer API yet (PRODUCT.7B2B does not
	// build one) — simulate it directly, as a human/future admin action would.
	if _, err := f.seed.Exec(context.Background(),
		`UPDATE crm_contact_company_evidence SET revoked_at = now() WHERE contact_id=$1 AND external_company_id='company-revoke-test'`,
		f.contactA); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := insert(); err != nil {
		t.Fatalf("re-verification after revocation: %v", err)
	}
	var active, historical int
	if err := platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
		sc, err := evidenceContext(sc, f.tenantA, f.userA)
		if err != nil {
			return err
		}
		return platformdb.QuerierFromContext(sc, f.app).QueryRow(sc, `
			SELECT count(*) FILTER (WHERE revoked_at IS NULL), count(*) FILTER (WHERE revoked_at IS NOT NULL)
			FROM crm_contact_company_evidence WHERE contact_id=$1 AND external_company_id='company-revoke-test'`,
			f.contactA).Scan(&active, &historical)
	}); err != nil {
		t.Fatal(err)
	}
	if active != 1 || historical != 1 {
		t.Fatalf("active=%d historical=%d, want exactly 1 and 1", active, historical)
	}
}

// O: deleting the origin ticket nulls ONLY origin_ticket_id — tenant_id
// (and the fact itself) survive. Uses a dedicated third tenant-A ticket so
// deleting it cannot affect any other test's fixture data.
func TestPostgresEvidenceStoreOriginTicketDeletionNullsOnlyThatColumn(t *testing.T) {
	f := setupEvidenceFixture(t)
	store := NewPostgresEvidenceStore(f.app)
	disposableTicket := uuid.New()
	if _, err := f.seed.Exec(context.Background(), `INSERT INTO tickets(id, tenant_id, conversation_id, status) VALUES($1,$2,$3,'closed')`,
		disposableTicket, f.tenantA, f.convA); err != nil {
		t.Fatal(err)
	}
	err := platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
		sc, err := evidenceContext(sc, f.tenantA, f.userA)
		if err != nil {
			return err
		}
		return store.RecordTicketSelection(sc, ports.RecordTicketSelectionInput{
			TenantID: f.tenantA, ContactID: f.contactA, ConnectionID: f.connA,
			ExternalCompanyID: "company-origin-ticket-delete", ActorUserID: f.userA,
			OriginTicketID: disposableTicket, OriginConversationID: f.convA,
		})
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := f.seed.Exec(context.Background(), `DELETE FROM tickets WHERE id=$1`, disposableTicket); err != nil {
		t.Fatalf("delete origin ticket: %v", err)
	}
	var tenantID uuid.UUID
	var originTicket *uuid.UUID
	if err := platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
		sc, err := evidenceContext(sc, f.tenantA, f.userA)
		if err != nil {
			return err
		}
		return platformdb.QuerierFromContext(sc, f.app).QueryRow(sc,
			`SELECT tenant_id, origin_ticket_id FROM crm_contact_company_evidence WHERE external_company_id='company-origin-ticket-delete'`,
		).Scan(&tenantID, &originTicket)
	}); err != nil {
		t.Fatal(err)
	}
	if tenantID != f.tenantA {
		t.Fatalf("tenant_id was nulled/changed by the origin ticket delete: got %s, want %s", tenantID, f.tenantA)
	}
	if originTicket != nil {
		t.Fatalf("origin_ticket_id = %v, want NULL after the origin ticket was deleted", *originTicket)
	}
}

// P: same proof for origin_conversation_id, using a dedicated conversation
// (deleting it never touches convA, used by other fixture data).
func TestPostgresEvidenceStoreOriginConversationDeletionNullsOnlyThatColumn(t *testing.T) {
	f := setupEvidenceFixture(t)
	store := NewPostgresEvidenceStore(f.app)
	disposableConv := uuid.New()
	if _, err := f.seed.Exec(context.Background(), `INSERT INTO conversations(id, tenant_id, contact_id) VALUES($1,$2,$3)`,
		disposableConv, f.tenantA, f.contactA); err != nil {
		t.Fatal(err)
	}
	err := platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
		sc, err := evidenceContext(sc, f.tenantA, f.userA)
		if err != nil {
			return err
		}
		return store.RecordTicketSelection(sc, ports.RecordTicketSelectionInput{
			TenantID: f.tenantA, ContactID: f.contactA, ConnectionID: f.connA,
			ExternalCompanyID: "company-origin-conv-delete", ActorUserID: f.userA,
			OriginTicketID: f.ticketA, OriginConversationID: disposableConv,
		})
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := f.seed.Exec(context.Background(), `DELETE FROM conversations WHERE id=$1`, disposableConv); err != nil {
		t.Fatalf("delete origin conversation: %v", err)
	}
	var tenantID uuid.UUID
	var originConv *uuid.UUID
	if err := platformdb.WithTenantSession(context.Background(), f.app, f.userA, false, func(sc context.Context) error {
		sc, err := evidenceContext(sc, f.tenantA, f.userA)
		if err != nil {
			return err
		}
		return platformdb.QuerierFromContext(sc, f.app).QueryRow(sc,
			`SELECT tenant_id, origin_conversation_id FROM crm_contact_company_evidence WHERE external_company_id='company-origin-conv-delete'`,
		).Scan(&tenantID, &originConv)
	}); err != nil {
		t.Fatal(err)
	}
	if tenantID != f.tenantA {
		t.Fatalf("tenant_id was nulled/changed by the origin conversation delete: got %s, want %s", tenantID, f.tenantA)
	}
	if originConv != nil {
		t.Fatalf("origin_conversation_id = %v, want NULL after the origin conversation was deleted", *originConv)
	}
}
