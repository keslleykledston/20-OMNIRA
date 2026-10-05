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

func (e *httpEnv) evidence(contact uuid.UUID, company string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	if _, err := e.seed.Exec(context.Background(), `
		INSERT INTO crm_contact_company_evidence (id, tenant_id, contact_id, connection_id, external_company_id, source, first_verified_at, last_verified_at)
		VALUES ($1,$2,$3,$4,$5,'ticket_selection', now(), now())`, id, e.tenant, contact, e.conn, company); err != nil {
		e.t.Fatal(err)
	}
	return id
}

type suggestionsResp struct {
	Items []struct {
		EvidenceID        uuid.UUID  `json:"evidence_id"`
		ExternalCompanyID string     `json:"external_company_id"`
		AccountID         *uuid.UUID `json:"account_id"`
		AccountName       *string    `json:"account_name"`
		AlreadyLinked     bool       `json:"already_linked"`
	} `json:"items"`
}

// ADR-0018: integration evidence is a SUGGESTION. It never classifies, never links, never calls the provider.
func TestIntegrationEvidenceIsOnlyASuggestionUntilAHumanAccepts(t *testing.T) {
	e := newHTTPEnv(t)
	c, other := e.contact("+5592922220101"), e.contact("+5592922220102")
	path := map[string]string{"contact_id": c.String()}
	ev := e.evidence(c, "42")
	e.evidence(other, "77")        // another contact's evidence
	revoked := e.evidence(c, "55") // revoked evidence
	if _, err := e.seed.Exec(context.Background(), `UPDATE crm_contact_company_evidence SET revoked_at=now() WHERE id=$1`, revoked); err != nil {
		t.Fatal(err)
	}
	// recording evidence changed NOTHING in the product classification
	if k := kindOf(t, e.seed, c); k != "unclassified" || e.n(`SELECT count(*) FROM contact_account_links WHERE contact_id=$1`, c) != 0 || e.n(`SELECT count(*) FROM customer_accounts WHERE tenant_id=$1`, e.tenant) != 0 {
		t.Fatalf("evidence must not classify, link or create accounts: kind=%s", k)
	}
	rec := e.do(e.agent, e.tenant, http.MethodGet, "", path, e.h.ListCompanySuggestions)
	var got suggestionsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 {
		t.Fatalf("suggestions = %d %s", rec.Code, rec.Body.String())
	}
	if len(got.Items) != 1 || got.Items[0].EvidenceID != ev || got.Items[0].ExternalCompanyID != "42" || got.Items[0].AccountID != nil || got.Items[0].AlreadyLinked {
		t.Fatalf("only this contact's ACTIVE evidence, with no invented account: %+v", got.Items)
	}
	// accepting it (a human click): directory validation + the link is recorded as ticket_flow; the decision is manual
	body := `{"kind":"customer","accounts":[{"directory_company_id":"42","evidence_id":"` + ev.String() + `","primary":true}]}`
	rec = e.do(e.agent, e.tenant, http.MethodPut, body, path, e.h.PutClassification)
	if rec.Code != 200 {
		t.Fatalf("accept = %d %s", rec.Code, rec.Body.String())
	}
	if e.n(`SELECT count(*) FROM contact_account_links WHERE contact_id=$1 AND source='ticket_flow' AND status='active'`, c) != 1 ||
		e.n(`SELECT count(*) FROM contacts WHERE id=$1 AND kind='customer' AND classification_source='manual'`, c) != 1 {
		t.Fatal("the link carries the evidence provenance (ticket_flow); the classification decision stays manual")
	}
	got = suggestionsResp{}
	_ = json.Unmarshal(e.do(e.agent, e.tenant, http.MethodGet, "", path, e.h.ListCompanySuggestions).Body.Bytes(), &got)
	if len(got.Items) != 1 || !got.Items[0].AlreadyLinked || got.Items[0].AccountName == nil || *got.Items[0].AccountName != "ACME Telecom" {
		t.Fatalf("after accepting, the suggestion shows the local account as linked: %+v", got.Items)
	}
}

func TestEvidenceBackedAcceptIsRevalidatedByTheServer(t *testing.T) {
	e := newHTTPEnv(t)
	c, other := e.contact("+5592922220103"), e.contact("+5592922220104")
	path := map[string]string{"contact_id": c.String()}
	good := e.evidence(c, "42")
	foreign := e.evidence(other, "42")
	revoked := e.evidence(c, "77")
	if _, err := e.seed.Exec(context.Background(), `UPDATE crm_contact_company_evidence SET revoked_at=now() WHERE id=$1`, revoked); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"evidence names another company":  `{"kind":"customer","accounts":[{"directory_company_id":"77","evidence_id":"` + good.String() + `"}]}`,
		"another contact's evidence":      `{"kind":"customer","accounts":[{"directory_company_id":"42","evidence_id":"` + foreign.String() + `"}]}`,
		"revoked evidence":                `{"kind":"customer","accounts":[{"directory_company_id":"77","evidence_id":"` + revoked.String() + `"}]}`,
		"unknown evidence":                `{"kind":"customer","accounts":[{"directory_company_id":"42","evidence_id":"` + uuid.NewString() + `"}]}`,
		"evidence without a directory id": `{"kind":"customer","accounts":[{"account_id":"` + uuid.NewString() + `","evidence_id":"` + good.String() + `"}]}`,
	} {
		rec := e.do(e.agent, e.tenant, http.MethodPut, body, path, e.h.PutClassification)
		if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 422/400", name, rec.Code)
		}
	}
	if kindOf(t, e.seed, c) != "unclassified" || e.n(`SELECT count(*) FROM contact_account_links WHERE tenant_id=$1`, e.tenant) != 0 || e.n(`SELECT count(*) FROM customer_accounts WHERE tenant_id=$1`, e.tenant) != 0 {
		t.Fatal("a refused evidence accept changed data")
	}
}

func TestSuggestionsRespectPermissionAndTenant(t *testing.T) {
	e := newHTTPEnv(t)
	other := seedTenant(t, e.seed, "clshttp-sugg-b")
	adminB := seedMemberRole(t, e.seed, other, "tenant_admin", "active")
	c := e.contact("+5592922220105")
	e.evidence(c, "42")
	path := map[string]string{"contact_id": c.String()}
	if rec := e.do(adminB, other, http.MethodGet, "", path, e.h.ListCompanySuggestions); rec.Code != http.StatusNotFound {
		t.Errorf("another tenant's contact = %d, want 404", rec.Code)
	}
	if rec := e.do(adminB, other, http.MethodGet, "", map[string]string{"contact_id": uuid.NewString()}, e.h.ListCompanySuggestions); rec.Code != http.StatusNotFound {
		t.Errorf("unknown contact = %d", rec.Code)
	}
	revoked := seedMemberRole(t, e.seed, e.tenant, "tenant_admin", "revoked")
	if rec := e.do(revoked, e.tenant, http.MethodGet, "", path, e.h.ListCompanySuggestions); rec.Code != http.StatusForbidden {
		t.Errorf("revoked = %d", rec.Code)
	}
}

// The contact's tickets say which account each one targets (ADR-0018), absent for tickets that predate accounts.
func TestContactTicketsExposeTheTargetAccount(t *testing.T) {
	e := newHTTPEnv(t)
	admin := seedMemberRole(t, e.seed, e.tenant, "tenant_admin", "active") // ticket.read
	c := e.contact("+5592922220201")
	conv1, conv2, acc := uuid.New(), uuid.New(), uuid.New()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO customer_accounts(id,tenant_id,name) VALUES($1,$2,'ACME Telecom')`, []any{acc, e.tenant}},
		{`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, []any{conv1, e.tenant, c}},
		{`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, []any{conv2, e.tenant, c}},
		{`INSERT INTO tickets(tenant_id,conversation_id,status,subject,provider,external_ticket_id,customer_account_id) VALUES($1,$2,'open','Com conta','k3g','1',$3)`, []any{e.tenant, conv1, acc}},
		{`INSERT INTO tickets(tenant_id,conversation_id,status,subject,provider,external_ticket_id) VALUES($1,$2,'open','Antigo','k3g','2')`, []any{e.tenant, conv2}},
	} {
		if _, err := e.seed.Exec(context.Background(), q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	h := NewContactsAPIHandler(e.app)
	rec := call(t, e.app, e.tenant, admin, http.MethodGet, "/", "", map[string]string{"contact_id": c.String()}, h.ListContactTickets)
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Items) != 2 {
		t.Fatalf("tickets = %d %s", rec.Code, rec.Body.String())
	}
	with, without := 0, 0
	for _, it := range page.Items {
		if it["subject"] == "Com conta" {
			if it["customer_account_id"] != acc.String() || it["customer_account_name"] != "ACME Telecom" {
				t.Errorf("account fields: %v", it)
			}
			with++
		} else {
			if _, has := it["customer_account_id"]; has {
				t.Errorf("a ticket that predates accounts must omit the field: %v", it)
			}
			without++
		}
	}
	if with != 1 || without != 1 {
		t.Fatalf("with=%d without=%d", with, without)
	}
}

// A switched-off feature answers 404 on every route, like every other flag (OMNIRA_CONTACT_CLASSIFICATION_ENABLED).
func TestClassificationAPIAnswers404WhenSwitchedOff(t *testing.T) {
	e := newHTTPEnv(t)
	c := e.contact("+5592922220301")
	path := map[string]string{"contact_id": c.String(), "link_id": uuid.NewString()}
	off := e.h.WithEnabled(false)
	for name, fn := range map[string]http.HandlerFunc{
		"get": off.GetClassification, "put": off.PutClassification, "link": off.LinkAccount,
		"end": off.EndLink, "primary": off.SetPrimary, "suggestions": off.ListCompanySuggestions,
	} {
		if rec := e.do(e.agent, e.tenant, http.MethodPost, `{"kind":"other"}`, path, fn); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 with the flag off", name, rec.Code)
		}
	}
	if kindOf(t, e.seed, c) != "unclassified" {
		t.Fatal("a switched-off API changed data")
	}
	// and back on: the same call works
	on := e.h.WithEnabled(true)
	if rec := e.do(e.agent, e.tenant, http.MethodPut, `{"kind":"other"}`, path, on.PutClassification); rec.Code != 200 || kindOf(t, e.seed, c) != "other" {
		t.Fatalf("flag on = %d", rec.Code)
	}
}
