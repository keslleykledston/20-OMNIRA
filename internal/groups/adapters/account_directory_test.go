package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketsports "github.com/omnira/omnira/internal/tickets/ports"
)

// ADR-0059 addendum 3: a groups-only tenant (e.g. K3G_Grupos) has no contacts/tickets to ever have created a
// customer_accounts row, so LinkAccount can also resolve the account straight from the tenant's own CRM directory
// instead of only from accounts already registered locally — exactly the find-or-create ClassificationHandler.resolveRef
// already does for contacts (internal/contacts/adapters/classification_http.go), reused here through
// accountsadapters.PostgresRepository.EnsureFromDirectoryPick.

type fakeCompanyDirectory struct{ companies []ticketsports.Company }

func (d fakeCompanyDirectory) ListCompanies(context.Context) ([]ticketsports.Company, error) {
	return d.companies, nil
}

// fakeRuntimeResolver hands back a distinct runtime (directory + connection) per tenant and fails closed
// (ResolutionNoConfiguration) for any tenant not registered — never a cross-tenant fallback.
type fakeRuntimeResolver struct {
	byTenant map[uuid.UUID]ticketsports.TicketingRuntime
}

func (r fakeRuntimeResolver) Resolve(_ context.Context, tenantID uuid.UUID) (*ticketsports.TicketingRuntime, error) {
	rt, ok := r.byTenant[tenantID]
	if !ok {
		return nil, &ticketsports.ResolutionError{Code: ticketsports.ResolutionNoConfiguration, Message: "no K3G connection configured for this tenant"}
	}
	return &rt, nil
}

// customRole creates a tenant-scoped role holding EXACTLY the given permissions (never the global fixed roles,
// which bundle many keys together) — the only way to prove a handler checks the KEY, not the role name.
func (e *env) customRole(tenant uuid.UUID, key string, perms ...string) uuid.UUID {
	role := uuid.New()
	e.exec(`INSERT INTO roles(id,tenant_id,key,name) VALUES($1,$2,$3,$4)`, role, tenant, key, key)
	for _, p := range perms {
		e.exec(`INSERT INTO role_permissions(role_id,permission_key) VALUES($1,$2)`, role, p)
	}
	return role
}

func (e *env) memberWithRole(tenant, role uuid.UUID, status string) uuid.UUID {
	u := uuid.New()
	e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid")
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u) })
	e.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,$4)`, tenant, u, role, status)
	return u
}

func (e *env) customerAccountCount(tenant uuid.UUID) int {
	return e.count(`SELECT count(*) FROM customer_accounts WHERE tenant_id=$1`, tenant)
}

func TestGroupAccountLinkFromCRMDirectoryCreatesAndReusesTheSameAccount(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	admin := e.member(tenantA, "tenant_admin", "active")
	g1 := e.group(tenantA, connA, jid1, "Grupo 1", true)

	resolver := fakeRuntimeResolver{byTenant: map[uuid.UUID]ticketsports.TicketingRuntime{
		tenantA: {ConnectionID: connA.ID, CompanyDirectory: fakeCompanyDirectory{companies: []ticketsports.Company{
			{ExternalID: "42", Name: "ACME Telecom", CNPJ: "11.111.111/0001-11", Active: true},
		}}},
	}}
	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), &fakeDir{}).WithCompanyDirectoryResolver(resolver)
	link := func(body string) *httptest.ResponseRecorder {
		return call(t, e.app, tenantA, admin, http.MethodPut, "/", body, map[string]string{"group_id": g1.String()}, h.LinkAccount)
	}

	if got := e.customerAccountCount(tenantA); got != 0 {
		t.Fatalf("before linking: %d customer_accounts, want 0", got)
	}

	var it groupItem
	rec := link(`{"directory_company_id":"42"}`)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &it) != nil || it.AccountID == nil || it.AccountName == nil || *it.AccountName != "ACME Telecom" {
		t.Fatalf("link via directory = %d %s", rec.Code, rec.Body.String())
	}
	firstAccount := *it.AccountID
	if got := e.customerAccountCount(tenantA); got != 1 {
		t.Fatalf("after first link: %d customer_accounts, want 1", got)
	}
	if got := e.count(`SELECT count(*) FROM account_external_links WHERE tenant_id=$1 AND account_id=$2 AND external_company_id='42' AND source='directory_selection'`, tenantA, firstAccount); got != 1 {
		t.Fatalf("account_external_links rows = %d, want 1", got)
	}
	var via *string
	if err := e.seed.QueryRow(context.Background(), `SELECT metadata->>'via' FROM audit_events WHERE tenant_id=$1 AND action='group.account_linked' ORDER BY created_at DESC LIMIT 1`, tenantA).Scan(&via); err != nil {
		t.Fatal(err)
	}
	if via == nil || *via != "crm_directory" {
		t.Fatalf("audit via = %v, want crm_directory", via)
	}

	// Same directory_company_id again (e.g. re-opening the picker and confirming the same company): idempotent,
	// reuses the SAME account — never a second customer_accounts row.
	rec = link(`{"directory_company_id":"42"}`)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &it) != nil || it.AccountID == nil || *it.AccountID != firstAccount {
		t.Fatalf("repeat link via directory = %d %s", rec.Code, rec.Body.String())
	}
	if got := e.customerAccountCount(tenantA); got != 1 {
		t.Fatalf("after repeat link: %d customer_accounts, want 1 (no duplicate)", got)
	}
	if got := e.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='group.account_linked'`, tenantA); got != 1 {
		t.Fatalf("repeat link must not re-audit: %d events, want 1", got)
	}
}

func TestGroupAccountLinkFromCRMDirectoryUnknownOrInactiveIs422(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	admin := e.member(tenantA, "tenant_admin", "active")
	g1 := e.group(tenantA, connA, jid1, "Grupo 1", true)

	resolver := fakeRuntimeResolver{byTenant: map[uuid.UUID]ticketsports.TicketingRuntime{
		tenantA: {ConnectionID: connA.ID, CompanyDirectory: fakeCompanyDirectory{companies: []ticketsports.Company{
			{ExternalID: "99", Name: "Empresa Inativa", Active: false},
		}}},
	}}
	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), &fakeDir{}).WithCompanyDirectoryResolver(resolver)
	link := func(body string) *httptest.ResponseRecorder {
		return call(t, e.app, tenantA, admin, http.MethodPut, "/", body, map[string]string{"group_id": g1.String()}, h.LinkAccount)
	}

	if rec := link(`{"directory_company_id":"unknown-id"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown company: %d, want 422", rec.Code)
	}
	if rec := link(`{"directory_company_id":"99"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("inactive company: %d, want 422", rec.Code)
	}
	if got := e.customerAccountCount(tenantA); got != 0 {
		t.Fatalf("customer_accounts after refused picks = %d, want 0 (nothing created)", got)
	}
	if got := e.count(`SELECT count(*) FROM wa_groups WHERE id=$1 AND account_id IS NULL`, g1); got != 1 {
		t.Fatal("the group must stay unlinked")
	}
}

func TestGroupAccountLinkFromCRMDirectoryWithoutResolverIs503(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	admin := e.member(tenantA, "tenant_admin", "active")
	g1 := e.group(tenantA, connA, jid1, "Grupo 1", true)

	// No WithCompanyDirectoryResolver call at all (main.go wiring never ran): must answer 503, never panic.
	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), &fakeDir{})
	rec := call(t, e.app, tenantA, admin, http.MethodPut, "/", `{"directory_company_id":"42"}`, map[string]string{"group_id": g1.String()}, h.LinkAccount)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no resolver configured: %d %s, want 503", rec.Code, rec.Body.String())
	}
	if got := e.customerAccountCount(tenantA); got != 0 {
		t.Fatalf("customer_accounts = %d, want 0", got)
	}
}

func TestGroupAccountLinkFromCRMDirectoryRejectsBothOrNeitherRef(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	admin := e.member(tenantA, "tenant_admin", "active")
	g1 := e.group(tenantA, connA, jid1, "Grupo 1", true)
	accountA := e.account(tenantA, "Cliente A")

	resolver := fakeRuntimeResolver{byTenant: map[uuid.UUID]ticketsports.TicketingRuntime{
		tenantA: {ConnectionID: connA.ID, CompanyDirectory: fakeCompanyDirectory{companies: []ticketsports.Company{{ExternalID: "42", Name: "ACME", Active: true}}}},
	}}
	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), &fakeDir{}).WithCompanyDirectoryResolver(resolver)
	link := func(body string) *httptest.ResponseRecorder {
		return call(t, e.app, tenantA, admin, http.MethodPut, "/", body, map[string]string{"group_id": g1.String()}, h.LinkAccount)
	}
	if rec := link(`{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("neither ref: %d, want 400", rec.Code)
	}
	if rec := link(`{"account_id":"` + accountA.String() + `","directory_company_id":"42"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("both refs: %d, want 400", rec.Code)
	}
}

// An operator of a groups-only tenant (ADR-0059: e.g. K3G_Grupos, built alongside K3G_Solutions while groups is
// developed) may hold ONLY group.manage — no ticket.create, no contact.classify, because that tenant has no
// contacts/tickets at all. The CRM directory route and the directory-linking path must both work for such an actor.
func TestGroupManageOnlyActorCanLinkAGroupFromTheCRMDirectory(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	role := e.customRole(tenantA, "groups_only", "group.manage")
	operator := e.memberWithRole(tenantA, role, "active")
	g1 := e.group(tenantA, connA, jid1, "Grupo 1", true)

	resolver := fakeRuntimeResolver{byTenant: map[uuid.UUID]ticketsports.TicketingRuntime{
		tenantA: {ConnectionID: connA.ID, CompanyDirectory: fakeCompanyDirectory{companies: []ticketsports.Company{{ExternalID: "42", Name: "ACME Telecom", Active: true}}}},
	}}
	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), &fakeDir{}).WithCompanyDirectoryResolver(resolver)

	// (b) linking the group through the directory succeeds for group.manage alone.
	rec := call(t, e.app, tenantA, operator, http.MethodPut, "/", `{"directory_company_id":"42"}`, map[string]string{"group_id": g1.String()}, h.LinkAccount)
	if rec.Code != http.StatusOK {
		t.Fatalf("group.manage-only link via directory: %d %s, want 200", rec.Code, rec.Body.String())
	}

	// (a) GET /crm/companies (the directory the picker reads) answers for the SAME actor/tenant, through the real
	// CRMHandlers.ListCompanies and the SAME runtime resolver (no second credential) — group.manage is now in the
	// route's accepted-keys list (internal/platform/httpserver/server.go) alongside ticket.create/contact.classify.
	crm := inboxadapters.NewCRMHandlers(e.app)
	crm.SetCompanyDirectoryResolver(resolver)
	err := platformdb.WithTenantSession(context.Background(), e.app, operator, false, func(sessionCtx context.Context) error {
		tc, tcErr := tenancydomain.NewTenantContext(tenantA, operator, tenancydomain.AccessSourceDirect)
		if tcErr != nil {
			return tcErr
		}
		ctx := authn.WithPrincipal(tenancydomain.WithTenantContext(sessionCtx, tc), &authn.Principal{UserID: operator})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+tenantA.String()+"/crm/companies", nil).WithContext(ctx)
		req.SetPathValue("tenant_id", tenantA.String())
		w := httptest.NewRecorder()
		crm.ListCompanies(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("group.manage-only GET /crm/companies: %d %s, want 200", w.Code, w.Body.String())
		}
		var resp struct {
			Items []struct{ Name string } `json:"items"`
		}
		if jsonErr := json.Unmarshal(w.Body.Bytes(), &resp); jsonErr != nil || len(resp.Items) != 1 || resp.Items[0].Name != "ACME Telecom" {
			t.Fatalf("crm companies body = %s", w.Body.String())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tenant session: %v", err)
	}
}

// The materialized account always stays in the LINKING group's own tenant, even when another tenant's directory
// happens to use the exact same external_company_id (CRM ids are per-tenant; accounts are never shared).
func TestGroupAccountLinkFromCRMDirectoryIsTenantIsolated(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	tenantB, connB := e.tenant()
	adminA := e.member(tenantA, "tenant_admin", "active")
	adminB := e.member(tenantB, "tenant_admin", "active")
	gA := e.group(tenantA, connA, jid1, "Grupo A", true)
	gB := e.group(tenantB, connB, jid2, "Grupo B", true)

	// Same external_company_id ("42") in both tenants' directories, on purpose: proves the two picks never
	// collide into one shared account even though the literal provider id string is identical.
	resolver := fakeRuntimeResolver{byTenant: map[uuid.UUID]ticketsports.TicketingRuntime{
		tenantA: {ConnectionID: connA.ID, CompanyDirectory: fakeCompanyDirectory{companies: []ticketsports.Company{{ExternalID: "42", Name: "ACME A", Active: true}}}},
		tenantB: {ConnectionID: connB.ID, CompanyDirectory: fakeCompanyDirectory{companies: []ticketsports.Company{{ExternalID: "42", Name: "ACME B", Active: true}}}},
	}}
	h := NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app), &fakeDir{}).WithCompanyDirectoryResolver(resolver)

	var itA, itB groupItem
	recA := call(t, e.app, tenantA, adminA, http.MethodPut, "/", `{"directory_company_id":"42"}`, map[string]string{"group_id": gA.String()}, h.LinkAccount)
	if recA.Code != http.StatusOK || json.Unmarshal(recA.Body.Bytes(), &itA) != nil || itA.AccountID == nil {
		t.Fatalf("tenant A link = %d %s", recA.Code, recA.Body.String())
	}
	recB := call(t, e.app, tenantB, adminB, http.MethodPut, "/", `{"directory_company_id":"42"}`, map[string]string{"group_id": gB.String()}, h.LinkAccount)
	if recB.Code != http.StatusOK || json.Unmarshal(recB.Body.Bytes(), &itB) != nil || itB.AccountID == nil {
		t.Fatalf("tenant B link = %d %s", recB.Code, recB.Body.String())
	}
	if *itA.AccountID == *itB.AccountID {
		t.Fatal("tenant A and tenant B must never resolve to the SAME local account")
	}
	if got := e.count(`SELECT count(*) FROM customer_accounts WHERE tenant_id=$1 AND id=$2`, tenantA, *itA.AccountID); got != 1 {
		t.Fatalf("tenant A's account must belong to tenant A: %d", got)
	}
	if got := e.count(`SELECT count(*) FROM customer_accounts WHERE tenant_id=$1 AND id=$2`, tenantB, *itB.AccountID); got != 1 {
		t.Fatalf("tenant B's account must belong to tenant B: %d", got)
	}
	if got := e.count(`SELECT count(*) FROM customer_accounts WHERE tenant_id=$1 AND id=$2`, tenantB, *itA.AccountID); got != 0 {
		t.Fatal("tenant A's account must never be visible/counted under tenant B")
	}

	// B can never link A's group, with or without a resolver configured — same 404 as an unknown group id.
	if rec := call(t, e.app, tenantB, adminB, http.MethodPut, "/", `{"directory_company_id":"42"}`, map[string]string{"group_id": gA.String()}, h.LinkAccount); rec.Code != http.StatusNotFound {
		t.Fatalf("tenant B linking tenant A's group via directory: %d, want 404", rec.Code)
	}
	if got := e.count(`SELECT count(*) FROM wa_groups WHERE id=$1`, gA); got != 1 {
		t.Fatal("tenant A's group row must still exist")
	}
}
