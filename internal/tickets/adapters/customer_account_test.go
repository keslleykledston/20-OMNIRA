package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	accountsadapters "github.com/omnira/omnira/internal/accounts/adapters"
	accountsdomain "github.com/omnira/omnira/internal/accounts/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
	"github.com/omnira/omnira/internal/tickets/ports"
)

// ADR-0018 Wave 7 on real Postgres: the ticket's customer account comes from the company the DIRECTORY validated,
// through account_external_links, and is set once.

type acctEnv struct {
	t         *testing.T
	seed, app *pgxpool.Pool
}

func newAcctEnv(t *testing.T) *acctEnv {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(seed.Close)
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return &acctEnv{t: t, seed: seed, app: app}
}

func (e *acctEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.seed.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *acctEnv) n(sql string, args ...any) (n int) {
	e.t.Helper()
	if err := e.seed.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return
}

type acctTenant struct{ id, user, conn, contact, conv, ticket uuid.UUID }

func (e *acctEnv) tenant() acctTenant {
	f := acctTenant{id: uuid.New(), user: uuid.New(), conn: uuid.New(), contact: uuid.New(), conv: uuid.New(), ticket: uuid.New()}
	e.exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, f.id, f.id.String())
	e.t.Cleanup(func() {
		_, _ = e.seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, f.id)
		_, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, f.user)
	})
	e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, f.user, f.user.String(), f.user.String()+"@invalid")
	var role uuid.UUID
	if err := e.seed.QueryRow(context.Background(), `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL`).Scan(&role); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, f.id, f.user, role)
	e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
	        VALUES($1,$2,'whatsapp','k3g','official',$3,'active','{}')`, f.conn, f.id, "n-"+f.conn.String())
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164,status) VALUES($1,$2,'C','+5592933330001','active')`, f.contact, f.id)
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, f.conv, f.id, f.contact)
	e.exec(`INSERT INTO tickets(id,tenant_id,conversation_id,status,subject) VALUES($1,$2,$3,'open','')`, f.ticket, f.id, f.conv)
	return f
}

func (e *acctEnv) in(f acctTenant, fn func(ctx context.Context)) {
	e.t.Helper()
	if err := platformdb.WithTenantSession(context.Background(), e.app, f.user, false, func(ctx context.Context) error {
		tc, err := tenancydomain.NewTenantContext(f.id, f.user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		fn(tenancydomain.WithTenantContext(ctx, tc))
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
}

func TestResolverMaterializesTheDirectoryCompanyOnceAndReusesIt(t *testing.T) {
	e := newAcctEnv(t)
	f := e.tenant()
	r := accountsadapters.NewTicketAccountResolver(e.app)
	acme := ports.Company{ExternalID: " 42 ", Name: "ACME Telecom", CNPJ: "11.111.111/0001-11", Active: true}
	var first, again uuid.UUID
	e.in(f, func(ctx context.Context) {
		var err error
		if first, err = r.ResolveForCompany(ctx, f.id, f.conn, acme); err != nil {
			t.Fatal(err)
		}
		if again, err = r.ResolveForCompany(ctx, f.id, f.conn, acme); err != nil {
			t.Fatal(err)
		}
	})
	if first == uuid.Nil || first != again || e.n(`SELECT count(*) FROM customer_accounts WHERE tenant_id=$1`, f.id) != 1 {
		t.Fatalf("one account per provider company: %v %v", first, again)
	}
	if e.n(`SELECT count(*) FROM account_external_links WHERE tenant_id=$1 AND account_id=$2 AND provider='k3g' AND connection_id=$3 AND external_company_id='42' AND source='ticket_flow' AND external_name_snapshot='ACME Telecom' AND verified_at IS NOT NULL`, f.id, first, f.conn) != 1 {
		t.Fatal("the link records provider, connection, trimmed company id, source and the directory's name")
	}
	// the cnpj is not stored anywhere on the account
	if e.n(`SELECT count(*) FROM customer_accounts WHERE id=$1 AND metadata::text LIKE '%11.111%'`, first) != 0 {
		t.Fatal("no provider metadata beyond the name snapshot")
	}
	// the same id on ANOTHER connection is another company -> another account
	conn2 := uuid.New()
	e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','k3g','official',$3,'active','{}')`, conn2, f.id, "m-"+conn2.String())
	e.in(f, func(ctx context.Context) {
		other, err := r.ResolveForCompany(ctx, f.id, conn2, acme)
		if err != nil || other == first {
			t.Fatalf("the same external id on another connection must not reuse the account: %v %v", other, err)
		}
	})
	// an inactive link is reactivated because the directory just said the company is active
	e.exec(`UPDATE account_external_links SET status='inactive' WHERE tenant_id=$1 AND connection_id=$2`, f.id, f.conn)
	e.in(f, func(ctx context.Context) {
		if id, err := r.ResolveForCompany(ctx, f.id, f.conn, acme); err != nil || id != first {
			t.Fatalf("reactivation: %v %v", id, err)
		}
	})
	if e.n(`SELECT count(*) FROM account_external_links WHERE tenant_id=$1 AND connection_id=$2 AND status='active'`, f.id, f.conn) != 1 {
		t.Fatal("the link must follow the directory (active again)")
	}
	// refused input
	e.in(f, func(ctx context.Context) {
		if _, err := r.ResolveForCompany(ctx, f.id, f.conn, ports.Company{ExternalID: "  "}); err == nil {
			t.Error("an empty company id must be refused")
		}
	})
}

func TestTicketCustomerAccountIsSetOnceAndNeverMoves(t *testing.T) {
	e := newAcctEnv(t)
	f := e.tenant()
	store := NewLocalTicketStore(e.app)
	repo := accountsadapters.NewPostgresRepository(e.app)
	var a1, a2 uuid.UUID
	e.in(f, func(ctx context.Context) {
		x, _ := repo.CreateAccount(ctx, f.id, "ACME", accountsdomain.TypeCustomer)
		y, _ := repo.CreateAccount(ctx, f.id, "XPTO", accountsdomain.TypeCustomer)
		a1, a2 = x.ID, y.ID
		if err := store.SetCustomerAccount(ctx, f.ticket, a1); err != nil {
			t.Fatal(err)
		}
		if err := store.SetCustomerAccount(ctx, f.ticket, a1); err != nil {
			t.Fatalf("setting the same account again is a no-op: %v", err)
		}
	})
	e.in(f, func(ctx context.Context) {
		// a failed statement aborts this transaction: do it last
		if err := store.SetCustomerAccount(ctx, f.ticket, a2); err == nil {
			t.Fatal("a ticket must never silently move to another customer")
		}
	})
	if e.n(`SELECT count(*) FROM tickets WHERE id=$1 AND customer_account_id=$2`, f.ticket, a1) != 1 {
		t.Fatal("the first account stays")
	}
	// another tenant's account is rejected by the composite FK even through the owner role
	other := e.tenant()
	var foreign uuid.UUID
	e.in(other, func(ctx context.Context) {
		z, _ := repo.CreateAccount(ctx, other.id, "De outro", accountsdomain.TypeCustomer)
		foreign = z.ID
	})
	if _, err := e.seed.Exec(context.Background(), `UPDATE tickets SET customer_account_id=$2 WHERE id=$1`, f.ticket, foreign); err == nil {
		t.Fatal("a ticket cannot target another tenant's account")
	}
}
