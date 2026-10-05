package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	ticketsports "github.com/omnira/omnira/internal/tickets/ports"
)

type fakeDirectory struct{ companies []ticketsports.Company }

func (d fakeDirectory) ListCompanies(context.Context) ([]ticketsports.Company, error) {
	return d.companies, nil
}

type fakeResolver struct {
	dir  fakeDirectory
	conn uuid.UUID
}

func (r fakeResolver) Resolve(context.Context, uuid.UUID) (*ticketsports.TicketingRuntime, error) {
	return &ticketsports.TicketingRuntime{CompanyDirectory: r.dir, ConnectionID: r.conn}, nil
}

type httpEnv struct {
	t         *testing.T
	seed, app *pgxpool.Pool
	h         *ClassificationHandler
	tenant    uuid.UUID
	agent     uuid.UUID
	conn      uuid.UUID
}

func newHTTPEnv(t *testing.T) *httpEnv {
	seed, app := seedPool(t), appPool(t)
	e := &httpEnv{t: t, seed: seed, app: app}
	e.tenant = seedTenant(t, seed, "clshttp")
	e.agent = seedMemberRole(t, seed, e.tenant, "tenant_agent", "active")
	e.conn = uuid.New()
	if _, err := seed.Exec(context.Background(), `INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
		VALUES($1,$2,'whatsapp','k3g','official',$3,'active','{}')`, e.conn, e.tenant, "n-"+e.conn.String()); err != nil {
		t.Fatal(err)
	}
	e.h = NewClassificationHandler(app, auditadapters.NewPostgresAuditEventRepository(app))
	e.h.SetCompanyDirectoryResolver(fakeResolver{conn: e.conn, dir: fakeDirectory{companies: []ticketsports.Company{
		{ExternalID: "42", Name: "ACME Telecom", CNPJ: "11.111.111/0001-11", Active: true},
		{ExternalID: "77", Name: "XPTO Redes", Active: true},
		{ExternalID: "55", Name: "Nova Empresa", Active: true},
		{ExternalID: "99", Name: "Empresa Inativa", Active: false},
	}}})
	return e
}

func (e *httpEnv) do(user uuid.UUID, tenant uuid.UUID, method, body string, path map[string]string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	return call(e.t, e.app, tenant, user, method, "/", body, path, fn)
}

func (e *httpEnv) n(sql string, args ...any) (n int) {
	if err := e.seed.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return
}

func (e *httpEnv) contact(phone string) uuid.UUID {
	return seedContact(e.t, e.seed, e.tenant, "Joana", phone, time.Now())
}

type viewResp struct {
	Kind                 string  `json:"kind"`
	ClassificationSource *string `json:"classification_source"`
	Accounts             []struct {
		ID, AccountID uuid.UUID
		AccountName   string `json:"account_name"`
		Primary       bool
		Status        string
	} `json:"accounts"`
}

func decodeView(t *testing.T, rec *httptest.ResponseRecorder) viewResp {
	t.Helper()
	var v viewResp
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestPutClassificationDirectoryCompanyIsValidatedServerSide(t *testing.T) {
	e := newHTTPEnv(t)
	c := e.contact("+5592922220001")
	path := map[string]string{"contact_id": c.String()}
	// the directory company becomes account + external link + customer link, with the DIRECTORY's name (not the browser's)
	rec := e.do(e.agent, e.tenant, http.MethodPut, `{"kind":"customer","accounts":[{"directory_company_id":"42","relationship_type":"employee","primary":true}]}`, path, e.h.PutClassification)
	v := decodeView(t, rec)
	if rec.Code != 200 || v.Kind != "customer" || len(v.Accounts) != 1 || v.Accounts[0].AccountName != "ACME Telecom" || !v.Accounts[0].Primary || v.ClassificationSource == nil || *v.ClassificationSource != "manual" {
		t.Fatalf("put = %d %s", rec.Code, rec.Body.String())
	}
	if e.n(`SELECT count(*) FROM account_external_links WHERE tenant_id=$1 AND provider='k3g' AND connection_id=$2 AND external_company_id='42' AND source='directory_selection' AND verified_at IS NOT NULL`, e.tenant, e.conn) != 1 {
		t.Fatal("the provider link must be recorded with the connection")
	}
	if e.n(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='contact.classified' AND resource_id=$2`, e.tenant, c.String()) != 1 {
		t.Fatal("first classification must be audited")
	}
	// the same company for ANOTHER contact reuses the account (no duplicate)
	c2 := e.contact("+5592922220002")
	rec = e.do(e.agent, e.tenant, http.MethodPut, `{"kind":"customer","accounts":[{"directory_company_id":"42"}]}`, map[string]string{"contact_id": c2.String()}, e.h.PutClassification)
	if rec.Code != 200 || e.n(`SELECT count(*) FROM customer_accounts WHERE tenant_id=$1`, e.tenant) != 1 {
		t.Fatalf("second contact = %d, accounts=%d", rec.Code, e.n(`SELECT count(*) FROM customer_accounts WHERE tenant_id=$1`, e.tenant))
	}
	// the same contact in a SECOND company: both stay, primary moves
	rec = e.do(e.agent, e.tenant, http.MethodPost, `{"directory_company_id":"77","primary":true}`, path, e.h.LinkAccount)
	if rec.Code != 200 {
		t.Fatalf("link 2nd company = %d %s", rec.Code, rec.Body.String())
	}
	v = decodeView(t, e.do(e.agent, e.tenant, http.MethodGet, "", path, e.h.GetClassification))
	if len(v.Accounts) != 2 {
		t.Fatalf("two companies expected: %+v", v.Accounts)
	}
	for _, a := range v.Accounts {
		if a.Primary != (a.AccountName == "XPTO Redes") {
			t.Fatalf("XPTO must be the only primary: %+v", v.Accounts)
		}
	}
	// refused entries leave NOTHING behind
	before := e.n(`SELECT count(*) FROM customer_accounts WHERE tenant_id=$1`, e.tenant)
	c3 := e.contact("+5592922220003")
	p3 := map[string]string{"contact_id": c3.String()}
	for name, body := range map[string]string{
		"inactive company":   `{"kind":"customer","accounts":[{"directory_company_id":"99"}]}`,
		"unknown company":    `{"kind":"customer","accounts":[{"directory_company_id":"nope"}]}`,
		"both refs":          `{"kind":"customer","accounts":[{"account_id":"` + uuid.NewString() + `","directory_company_id":"42"}]}`,
		"no ref":             `{"kind":"customer","accounts":[{}]}`,
		"browser name":       `{"kind":"customer","accounts":[{"directory_company_id":"42","name":"Fake","cnpj":"1"}]}`,
		"no accounts":        `{"kind":"customer"}`,
		"two primaries":      `{"kind":"customer","accounts":[{"directory_company_id":"42","primary":true},{"directory_company_id":"77","primary":true}]}`,
		"foreign account":    `{"kind":"customer","accounts":[{"account_id":"` + uuid.NewString() + `"}]}`,
		"valid then foreign": `{"kind":"customer","accounts":[{"directory_company_id":"55"},{"account_id":"` + uuid.NewString() + `"}]}`,
		"bad kind":           `{"kind":"agent","accounts":[]}`,
		"tenant in payload":  `{"kind":"other","tenant_id":"` + uuid.NewString() + `"}`,
	} {
		rec := e.do(e.agent, e.tenant, http.MethodPut, body, p3, e.h.PutClassification)
		if rec.Code < 400 {
			t.Errorf("%s = %d, want an error", name, rec.Code)
		}
	}
	if k := kindOf(t, e.seed, c3); k != "unclassified" || e.n(`SELECT count(*) FROM customer_accounts WHERE tenant_id=$1`, e.tenant) != before || e.n(`SELECT count(*) FROM contact_account_links WHERE contact_id=$1`, c3) != 0 {
		t.Fatalf("refused requests changed data: kind=%s", k)
	}
	if e.n(`SELECT count(*) FROM account_external_links WHERE tenant_id=$1 AND external_company_id IN ('99','nope','55')`, e.tenant) != 0 {
		t.Fatal("an inactive/unknown company must never be materialized")
	}
}

func TestEndLinkLastCompanyNeedsReclassifyAndIsAudited(t *testing.T) {
	e := newHTTPEnv(t)
	c := e.contact("+5592922220004")
	path := map[string]string{"contact_id": c.String()}
	rec := e.do(e.agent, e.tenant, http.MethodPut, `{"kind":"customer","accounts":[{"directory_company_id":"42"}]}`, path, e.h.PutClassification)
	v := decodeView(t, rec)
	if rec.Code != 200 || len(v.Accounts) != 1 {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}
	endPath := map[string]string{"contact_id": c.String(), "link_id": v.Accounts[0].ID.String()}
	if rec := e.do(e.agent, e.tenant, http.MethodPost, ``, endPath, e.h.EndLink); rec.Code != http.StatusConflict {
		t.Fatalf("last link without reclassify = %d, want 409", rec.Code)
	}
	if rec := e.do(e.agent, e.tenant, http.MethodPost, `{"reclassify_to":"customer"}`, endPath, e.h.EndLink); rec.Code != http.StatusBadRequest {
		t.Fatalf("reclassify_to customer = %d, want 400", rec.Code)
	}
	if kindOf(t, e.seed, c) != "customer" || e.n(`SELECT count(*) FROM contact_account_links WHERE contact_id=$1 AND status='active'`, c) != 1 {
		t.Fatal("a refused removal changed data")
	}
	rec = e.do(e.agent, e.tenant, http.MethodPost, `{"reclassify_to":"other"}`, endPath, e.h.EndLink)
	v = decodeView(t, rec)
	if rec.Code != 200 || v.Kind != "other" || len(v.Accounts) != 0 {
		t.Fatalf("end+reclassify = %d %s", rec.Code, rec.Body.String())
	}
	if e.n(`SELECT count(*) FROM contact_account_links WHERE contact_id=$1 AND status='ended' AND ended_at IS NOT NULL`, c) != 1 {
		t.Fatal("the link must be soft-ended")
	}
	for _, a := range []string{"contact.account_unlinked", "contact.reclassified", "contact.classified"} {
		if e.n(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2 AND resource_id=$3`, e.tenant, a, c.String()) < 1 {
			t.Errorf("missing audit %s", a)
		}
	}
}

func TestClassificationAPIPermissionsAndTenantIsolation(t *testing.T) {
	e := newHTTPEnv(t)
	other := seedTenant(t, e.seed, "clshttp-b")
	adminB := seedMemberRole(t, e.seed, other, "tenant_admin", "active")
	revoked := seedMemberRole(t, e.seed, e.tenant, "tenant_admin", "revoked")
	// a role WITHOUT contact.classify / account.read
	viewer, role := uuid.New(), uuid.New()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users(id,external_subject,email,status) VALUES($1,$3,$2,'active')`, []any{viewer, viewer.String() + "@invalid", viewer.String()}},
		{`INSERT INTO roles(id,tenant_id,key,name) VALUES($1,$2,'viewer_only','Viewer')`, []any{role, e.tenant}},
		{`INSERT INTO role_permissions(role_id,permission_key) VALUES($1,'tenant.read')`, []any{role}},
		{`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, []any{e.tenant, viewer, role}},
	} {
		if _, err := e.seed.Exec(context.Background(), q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, viewer) })
	c := e.contact("+5592922220005")
	path := map[string]string{"contact_id": c.String()}
	body := `{"kind":"customer","accounts":[{"directory_company_id":"42"}]}`
	if rec := e.do(viewer, e.tenant, http.MethodPut, body, path, e.h.PutClassification); rec.Code != http.StatusForbidden {
		t.Errorf("viewer put = %d", rec.Code)
	}
	if rec := e.do(viewer, e.tenant, http.MethodGet, "", path, e.h.GetClassification); rec.Code != http.StatusForbidden {
		t.Errorf("viewer get = %d", rec.Code)
	}
	if rec := e.do(revoked, e.tenant, http.MethodPut, body, path, e.h.PutClassification); rec.Code != http.StatusForbidden {
		t.Errorf("revoked put = %d", rec.Code)
	}
	// B's admin, acting inside B, cannot touch A's contact: same 404 as an unknown id
	foreign := e.do(adminB, other, http.MethodPut, `{"kind":"other"}`, path, e.h.PutClassification)
	unknown := e.do(adminB, other, http.MethodPut, `{"kind":"other"}`, map[string]string{"contact_id": uuid.NewString()}, e.h.PutClassification)
	if foreign.Code != http.StatusNotFound || unknown.Code != http.StatusNotFound || foreign.Body.String() != unknown.Body.String() {
		t.Fatalf("enumeration oracle: %d %q vs %d %q", foreign.Code, foreign.Body.String(), unknown.Code, unknown.Body.String())
	}
	if rec := e.do(adminB, other, http.MethodGet, "", path, e.h.GetClassification); rec.Code != http.StatusNotFound {
		t.Errorf("foreign get = %d", rec.Code)
	}
	// A's account UUID is not usable from B
	var acc uuid.UUID
	e.do(e.agent, e.tenant, http.MethodPut, body, path, e.h.PutClassification)
	if err := e.seed.QueryRow(context.Background(), `SELECT id FROM customer_accounts WHERE tenant_id=$1`, e.tenant).Scan(&acc); err != nil {
		t.Fatal(err)
	}
	cB := seedContact(t, e.seed, other, "Contato B", "+5592922220006", time.Now())
	rec := e.do(adminB, other, http.MethodPut, `{"kind":"customer","accounts":[{"account_id":"`+acc.String()+`"}]}`, map[string]string{"contact_id": cB.String()}, e.h.PutClassification)
	if rec.Code != http.StatusUnprocessableEntity || kindOf(t, e.seed, cB) != "unclassified" {
		t.Fatalf("foreign account UUID = %d kind=%s", rec.Code, kindOf(t, e.seed, cB))
	}
	// the foreign link UUID cannot be ended from the other tenant either
	var link uuid.UUID
	_ = e.seed.QueryRow(context.Background(), `SELECT id FROM contact_account_links WHERE contact_id=$1`, c).Scan(&link)
	if rec := e.do(adminB, other, http.MethodPost, `{"reclassify_to":"other"}`, map[string]string{"contact_id": cB.String(), "link_id": link.String()}, e.h.EndLink); rec.Code != http.StatusNotFound {
		t.Errorf("foreign link end = %d", rec.Code)
	}
	if !strings.Contains(kindOf(t, e.seed, c), "customer") {
		t.Fatal("A's contact changed")
	}
}
