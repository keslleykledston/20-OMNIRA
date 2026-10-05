package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/accounts/domain"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
)

type env struct {
	t    *testing.T
	seed *pgxpool.Pool
	app  *pgxpool.Pool
}

func newEnv(t *testing.T) *env {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	return &env{t: t, seed: seed, app: app}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.seed.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) count(sql string, args ...any) (n int) {
	e.t.Helper()
	if err := e.seed.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (e *env) tenant() uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, id, id.String())
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, id) })
	return id
}

func (e *env) member(tenant uuid.UUID, role, status string) uuid.UUID {
	u := uuid.New()
	e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid")
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u) })
	var roleID uuid.UUID
	if err := e.seed.QueryRow(context.Background(), `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, role).Scan(&roleID); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,$4)`, tenant, u, roleID, status)
	return u
}

func (e *env) connection(tenant uuid.UUID) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
	        VALUES($1,$2,'whatsapp','k3g','official',$3,'active','{}')`, id, tenant, "n-"+id.String())
	return id
}

func (e *env) call(tenant, user uuid.UUID, method, body string, path map[string]string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := httptest.NewRecorder()
	_ = platformdb.WithTenantSession(context.Background(), e.app, user, false, func(ctx context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		req := httptest.NewRequest(method, "/?"+path["query"], bytes.NewReader([]byte(body))).WithContext(tenancydomain.WithTenantContext(ctx, tc))
		for k, v := range path {
			if k != "query" {
				req.SetPathValue(k, v)
			}
		}
		fn(rec, req)
		return nil
	})
	return rec
}

func (e *env) attempt(tenant, user uuid.UUID, fn func(ctx context.Context)) {
	e.t.Helper()
	_ = platformdb.WithTenantSession(context.Background(), e.app, user, false, func(ctx context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		fn(tenancydomain.WithTenantContext(ctx, tc))
		return nil
	})
}

func (e *env) session(tenant, user uuid.UUID, fn func(ctx context.Context)) {
	e.t.Helper()
	if err := platformdb.WithTenantSession(context.Background(), e.app, user, false, func(ctx context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		fn(tenancydomain.WithTenantContext(ctx, tc))
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func TestAccountLifecycleThroughTheAPI(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a, "tenant_admin", "active")
	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app))

	rec := e.call(a, admin, http.MethodPost, `{"name":"  ACME Telecom  "}`, nil, h.CreateAccount)
	var created accountDTO
	decodeBody(t, rec, &created)
	if rec.Code != http.StatusCreated || created.Name != "ACME Telecom" || created.Type != "customer" || created.Status != "active" {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	if e.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='account.created' AND resource_id=$2`, a, created.ID) != 1 {
		t.Fatal("creation must be audited")
	}
	idp := map[string]string{"account_id": created.ID.String()}
	// rename, change type, deactivate, archive (hidden from the default list) and restore
	for _, c := range []struct{ body, status, want string }{
		{`{"name":"ACME Telecom S.A.","account_type":"partner"}`, "active", "partner"},
		{`{"status":"inactive"}`, "inactive", "partner"},
		{`{"status":"archived"}`, "archived", "partner"},
	} {
		rec = e.call(a, admin, http.MethodPatch, c.body, idp, h.UpdateAccount)
		var got accountDTO
		decodeBody(t, rec, &got)
		if rec.Code != 200 || got.Status != c.status || got.Type != c.want {
			t.Fatalf("patch %s = %d %s", c.body, rec.Code, rec.Body.String())
		}
	}
	if e.count(`SELECT count(*) FROM customer_accounts WHERE id=$1 AND archived_at IS NOT NULL`, created.ID) != 1 {
		t.Fatal("archiving must stamp archived_at")
	}
	list := func(q string) int {
		var out struct{ Items []accountDTO }
		decodeBody(t, e.call(a, admin, http.MethodGet, "", map[string]string{"query": q}, h.ListAccounts), &out)
		return len(out.Items)
	}
	if list("") != 0 || list("status=archived") != 1 || list("q=acme&status=archived") != 1 || list("q=xpto&status=archived") != 0 {
		t.Fatal("archived accounts are hidden by default and found by status/search")
	}
	rec = e.call(a, admin, http.MethodPatch, `{"status":"active"}`, idp, h.UpdateAccount)
	if rec.Code != 200 || e.count(`SELECT count(*) FROM customer_accounts WHERE id=$1 AND archived_at IS NULL AND status='active'`, created.ID) != 1 || list("") != 1 {
		t.Fatalf("restore = %d", rec.Code)
	}
	// validation: nothing is stored for refused input
	for name, body := range map[string]string{"empty name": `{"name":"   "}`, "bad type": `{"name":"X","account_type":"vip"}`, "unknown field": `{"name":"X","tenant_id":"` + a.String() + `"}`, "not json": `nope`} {
		if rec := e.call(a, admin, http.MethodPost, body, nil, h.CreateAccount); rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d", name, rec.Code)
		}
	}
	if rec := e.call(a, admin, http.MethodPatch, `{"status":"deleted"}`, idp, h.UpdateAccount); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad status = %d", rec.Code)
	}
	if e.count(`SELECT count(*) FROM customer_accounts WHERE tenant_id=$1`, a) != 1 {
		t.Fatal("refused requests created accounts")
	}
}

func TestAccountPermissionsComeFromTheMatrix(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin, agent := e.member(a, "tenant_admin", "active"), e.member(a, "tenant_agent", "active")
	revoked := e.member(a, "tenant_admin", "revoked")
	h := NewHandler(e.app, nil)
	var acc accountDTO
	decodeBody(t, e.call(a, admin, http.MethodPost, `{"name":"ACME"}`, nil, h.CreateAccount), &acc)
	idp := map[string]string{"account_id": acc.ID.String()}

	// an attending agent reads (account.read) but cannot manage (account.manage)
	if rec := e.call(a, agent, http.MethodGet, "", nil, h.ListAccounts); rec.Code != 200 {
		t.Errorf("agent list = %d", rec.Code)
	}
	if rec := e.call(a, agent, http.MethodGet, "", idp, h.GetAccount); rec.Code != 200 {
		t.Errorf("agent get = %d", rec.Code)
	}
	if rec := e.call(a, agent, http.MethodPost, `{"name":"X"}`, nil, h.CreateAccount); rec.Code != http.StatusForbidden {
		t.Errorf("agent create = %d, want 403", rec.Code)
	}
	if rec := e.call(a, agent, http.MethodPatch, `{"status":"archived"}`, idp, h.UpdateAccount); rec.Code != http.StatusForbidden {
		t.Errorf("agent patch = %d, want 403", rec.Code)
	}
	// a revoked membership has nothing
	for name, fn := range map[string]http.HandlerFunc{"list": h.ListAccounts, "get": h.GetAccount} {
		if rec := e.call(a, revoked, http.MethodGet, "", idp, fn); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Errorf("revoked %s = %d", name, rec.Code)
		}
	}
	if e.count(`SELECT count(*) FROM customer_accounts WHERE tenant_id=$1 AND status='archived'`, a) != 0 {
		t.Fatal("a forbidden call changed data")
	}
}

func TestAccountsAreInvisibleAcrossTenantsAndRuntimeRoleCannotEraseThem(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	adminA, adminB := e.member(a, "tenant_admin", "active"), e.member(b, "tenant_admin", "active")
	h := NewHandler(e.app, nil)
	var accB accountDTO
	decodeBody(t, e.call(b, adminB, http.MethodPost, `{"name":"Empresa de B"}`, nil, h.CreateAccount), &accB)
	idp := map[string]string{"account_id": accB.ID.String()}

	// A knows B's account UUID: everything is 404, nothing leaks
	if rec := e.call(a, adminA, http.MethodGet, "", idp, h.GetAccount); rec.Code != http.StatusNotFound {
		t.Errorf("get foreign = %d", rec.Code)
	}
	if rec := e.call(a, adminA, http.MethodPatch, `{"name":"sequestrada"}`, idp, h.UpdateAccount); rec.Code != http.StatusNotFound {
		t.Errorf("patch foreign = %d", rec.Code)
	}
	var out struct{ Items []accountDTO }
	decodeBody(t, e.call(a, adminA, http.MethodGet, "", map[string]string{"query": "q=Empresa"}, h.ListAccounts), &out)
	if len(out.Items) != 0 {
		t.Fatal("tenant A listed tenant B's account")
	}
	var name string
	_ = e.seed.QueryRow(context.Background(), `SELECT name FROM customer_accounts WHERE id=$1`, accB.ID).Scan(&name)
	if name != "Empresa de B" {
		t.Fatalf("B's account changed: %q", name)
	}
	// without a tenant session the runtime role sees nothing and can delete nothing (no DELETE policy / grant)
	var n int
	_ = e.app.QueryRow(context.Background(), `SELECT count(*) FROM customer_accounts`).Scan(&n)
	if n != 0 {
		t.Fatalf("a session without a tenant sees %d accounts", n)
	}
	// even inside B's own session the runtime role cannot erase an account: no DELETE policy, so 0 rows are affected
	e.session(b, adminB, func(ctx context.Context) {
		tag, err := platformdb.QuerierFromContext(ctx, e.app).Exec(ctx, `DELETE FROM customer_accounts WHERE id=$1`, accB.ID)
		if err == nil && tag.RowsAffected() != 0 {
			t.Errorf("the runtime role deleted %d accounts", tag.RowsAffected())
		}
	})
	if e.count(`SELECT count(*) FROM customer_accounts WHERE id=$1`, accB.ID) != 1 {
		t.Fatal("the account was erased")
	}
	// the repository refuses to attach a link of one tenant to an account of another, and a foreign connection
	repo := NewPostgresRepository(e.app)
	connA := e.connection(a)
	e.session(a, adminA, func(ctx context.Context) {
		if _, err := repo.UpsertExternalLink(ctx, domain.ExternalLink{TenantID: a, AccountID: accB.ID, Provider: "k3g", ConnectionID: connA, ExternalCompanyID: "1", Source: domain.SourceDirectorySelection}); !errors.Is(err, domain.ErrAccountNotFound) {
			t.Errorf("link to a foreign account: %v", err)
		}
	})
}

func TestExternalLinksAreScopedByProviderAndConnection(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	admin := e.member(a, "tenant_admin", "active")
	repo := NewPostgresRepository(e.app)
	conn1, conn2, foreign := e.connection(a), e.connection(a), e.connection(b)
	e.session(a, admin, func(ctx context.Context) {
		acme, err := repo.CreateAccount(ctx, a, "ACME", domain.TypeCustomer)
		if err != nil {
			t.Fatal(err)
		}
		xpto, _ := repo.CreateAccount(ctx, a, "XPTO", domain.TypeCustomer)
		snap := "ACME TELECOM LTDA"
		link := domain.ExternalLink{TenantID: a, AccountID: acme.ID, Provider: "k3g", ConnectionID: conn1, ExternalCompanyID: " 42 ", ExternalNameSnapshot: &snap, Source: domain.SourceDirectorySelection}
		first, err := repo.UpsertExternalLink(ctx, link)
		if err != nil || first.ExternalCompanyID != "42" || first.Status != domain.LinkActive {
			t.Fatalf("link: %+v %v", first, err)
		}
		// idempotent: the same company on the same account returns the same link
		again, err := repo.UpsertExternalLink(ctx, link)
		if err != nil || again.ID != first.ID {
			t.Fatalf("replay must return the existing link: %+v %v", again, err)
		}
		if n := len(mustLinks(t, repo, ctx, a, acme.ID)); n != 1 {
			t.Fatalf("replay created %d links", n)
		}
		// the same external id on the SAME connection cannot belong to another account
		taken := link
		taken.AccountID = xpto.ID
		if _, err := repo.UpsertExternalLink(ctx, taken); !errors.Is(err, domain.ErrExternalLinkTaken) {
			t.Fatalf("duplicate company on another account: %v", err)
		}
		// ...but the same value on ANOTHER connection is another company
		other := link
		other.AccountID, other.ConnectionID = xpto.ID, conn2
		if l, err := repo.UpsertExternalLink(ctx, other); err != nil || l.AccountID != xpto.ID {
			t.Fatalf("same external id on another connection must be allowed: %v", err)
		}
		// lookup by (provider, connection, id)
		if l, err := repo.FindByExternal(ctx, a, "k3g", conn1, "42"); err != nil || l == nil || l.AccountID != acme.ID {
			t.Fatalf("find: %+v %v", l, err)
		}
		if l, _ := repo.FindByExternal(ctx, a, "k3g", conn2, "42"); l == nil || l.AccountID != xpto.ID {
			t.Fatal("the same id resolves per connection")
		}
		if l, _ := repo.FindByExternal(ctx, a, "other-provider", conn1, "42"); l != nil {
			t.Fatal("provider is part of the identity")
		}
		// a provider company turned inactive keeps its link (history), only its status changes
		if err := repo.SetLinkStatus(ctx, a, first.ID, domain.LinkInactive); err != nil {
			t.Fatal(err)
		}
		if l, _ := repo.FindByExternal(ctx, a, "k3g", conn1, "42"); l == nil || l.Status != domain.LinkInactive || l.AccountID != acme.ID {
			t.Fatal("an inactive provider company must keep its link")
		}
	})
	// a connection of another tenant is refused (own session: the FK error aborts its transaction)
	e.attempt(a, admin, func(ctx context.Context) {
		acc, err := repo.CreateAccount(ctx, a, "Z", domain.TypeOther)
		if err != nil {
			t.Fatal(err)
		}
		bad := domain.ExternalLink{TenantID: a, AccountID: acc.ID, Provider: "k3g", ConnectionID: foreign, ExternalCompanyID: "99", Source: domain.SourceDirectorySelection}
		if _, err := repo.UpsertExternalLink(ctx, bad); !errors.Is(err, domain.ErrInvalidAccount) {
			t.Errorf("foreign connection: %v", err)
		}
	})
	// the database itself refuses a duplicate (defence in depth)
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO account_external_links(tenant_id,account_id,provider,connection_id,external_company_id,source)
		SELECT tenant_id, account_id, provider, connection_id, external_company_id, 'import' FROM account_external_links WHERE tenant_id=$1 LIMIT 1`, a); err == nil {
		t.Fatal("UNIQUE(tenant, provider, connection, external id) must hold")
	}
}

func mustLinks(t *testing.T, repo *PostgresRepository, ctx context.Context, tenant, account uuid.UUID) []domain.ExternalLink {
	t.Helper()
	l, err := repo.ListExternalLinks(ctx, tenant, account)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestAccountNamesAreNormalizedAndBounded(t *testing.T) {
	for in, ok := range map[string]bool{"ACME": true, "  x  ": true, "": false, "   ": false} {
		if _, err := domain.NormalizeName(in); (err == nil) != ok {
			t.Errorf("%q ok=%v", in, err == nil)
		}
	}
	long := make([]rune, domain.MaxNameRunes+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := domain.NormalizeName(string(long)); err == nil {
		t.Error("a name over the limit must be refused")
	}
	if !domain.TypeInternal.Valid() || domain.AccountType("person").Valid() {
		t.Error("types")
	}
}

func TestAccountTicketsAreTheRealTicketsThatTargetIt(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	admin, agent := e.member(a, "tenant_admin", "active"), e.member(a, "tenant_agent", "active")
	adminB := e.member(b, "tenant_admin", "active")
	h := NewHandler(e.app, nil)
	var acme, xpto accountDTO
	decodeBody(t, e.call(a, admin, http.MethodPost, `{"name":"ACME"}`, nil, h.CreateAccount), &acme)
	decodeBody(t, e.call(a, admin, http.MethodPost, `{"name":"XPTO"}`, nil, h.CreateAccount), &xpto)
	contact, conv1, conv2, conv3 := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164,status) VALUES($1,$2,'C','+5592944440001','active')`, contact, a)
	for _, c := range []uuid.UUID{conv1, conv2, conv3} {
		e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, c, a, contact)
	}
	real, placeholder, other := uuid.New(), uuid.New(), uuid.New()
	e.exec(`INSERT INTO tickets(id,tenant_id,conversation_id,status,subject,provider,external_ticket_id,customer_account_id) VALUES($1,$2,$3,'open','Link caiu','k3g','28180',$4)`, real, a, conv1, acme.ID)
	e.exec(`INSERT INTO tickets(id,tenant_id,conversation_id,status,subject,customer_account_id) VALUES($1,$2,$3,'open','',$4)`, placeholder, a, conv2, acme.ID) // empty placeholder: not a real ticket
	e.exec(`INSERT INTO tickets(id,tenant_id,conversation_id,status,subject,provider,external_ticket_id,customer_account_id) VALUES($1,$2,$3,'open','Outro','k3g','28181',$4)`, other, a, conv3, xpto.ID)
	idp := map[string]string{"account_id": acme.ID.String()}
	var out struct{ Items []accountTicketDTO }
	rec := e.call(a, admin, http.MethodGet, "", idp, h.ListAccountTickets)
	decodeBody(t, rec, &out)
	if rec.Code != 200 || len(out.Items) != 1 || out.Items[0].ID != real {
		t.Fatalf("only the real ticket of THIS account: %d %s", rec.Code, rec.Body.String())
	}
	// tenant-wide ticket visibility is its own grant: an agent (account.read, no ticket.read) is refused
	if rec := e.call(a, agent, http.MethodGet, "", idp, h.ListAccountTickets); rec.Code != http.StatusForbidden {
		t.Errorf("agent = %d, want 403", rec.Code)
	}
	// another tenant's admin gets the same 404 as an unknown account
	if rec := e.call(b, adminB, http.MethodGet, "", idp, h.ListAccountTickets); rec.Code != http.StatusNotFound {
		t.Errorf("foreign = %d", rec.Code)
	}
	if rec := e.call(a, admin, http.MethodGet, "", map[string]string{"account_id": uuid.NewString()}, h.ListAccountTickets); rec.Code != http.StatusNotFound {
		t.Errorf("unknown = %d", rec.Code)
	}
}
