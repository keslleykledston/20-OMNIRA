package adapters

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// ADR-0018 Wave 8: the company context of a SUBJECT, on real Postgres under FORCE RLS.

type ctxResp struct {
	Status  string `json:"status"`
	Primary *struct {
		AccountID       uuid.UUID `json:"account_id"`
		Name            string    `json:"name"`
		Source          string    `json:"source"`
		Persisted       bool      `json:"persisted"`
		LinkedToContact bool      `json:"linked_to_contact"`
	} `json:"primary"`
	Related []struct {
		AccountID uuid.UUID `json:"account_id"`
		Name      string    `json:"name"`
	} `json:"related"`
	Candidates []struct {
		AccountID      uuid.UUID `json:"account_id"`
		Name           string    `json:"name"`
		ContactPrimary bool      `json:"contact_primary"`
	} `json:"candidates"`
}

func (e *env) account(tenant uuid.UUID, name, status string) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO customer_accounts(id,tenant_id,name,status,archived_at) VALUES($1,$2,$3,$4, CASE WHEN $4='archived' THEN now() END)`, id, tenant, name, status)
	return id
}

func (e *env) contactLink(tenant, contact, account uuid.UUID, primary bool) {
	e.exec(`INSERT INTO contact_account_links(tenant_id,contact_id,account_id,source,is_primary) VALUES($1,$2,$3,'manual',$4)`, tenant, contact, account, primary)
}

func (e *env) acctCtx(tenant, user, topic uuid.UUID) (int, ctxResp) {
	e.t.Helper()
	h := e.handler()
	rec := e.call(tenant, user, http.MethodGet, "", map[string]string{"topic_id": topic.String()}, h.GetAccountContext)
	var out ctxResp
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			e.t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

func (e *env) linkTopic(tenant, user, topic uuid.UUID, body string) int {
	e.t.Helper()
	h := e.handler()
	return e.call(tenant, user, http.MethodPost, body, map[string]string{"topic_id": topic.String()}, h.LinkAccount).Code
}

func TestTopicAccountContextNeverGuessesAmongSeveralCompanies(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	topic, _ := e.topicWith(a, admin, "Pedido atrasado", "meu pedido atrasou")

	// no company at all
	if code, c := e.acctCtx(a.id, admin, topic.ID); code != 200 || c.Status != "none" || c.Primary != nil || len(c.Candidates) != 0 {
		t.Fatalf("no companies: %d %+v", code, c)
	}
	// exactly one company: resolved, but DERIVED (nothing is stored)
	acme := e.account(a.id, "ACME", "active")
	e.contactLink(a.id, a.contact, acme, true)
	code, c := e.acctCtx(a.id, admin, topic.ID)
	if code != 200 || c.Status != "resolved" || c.Primary == nil || c.Primary.AccountID != acme || c.Primary.Source != "sole_company" || c.Primary.Persisted {
		t.Fatalf("sole company: %d %+v", code, c)
	}
	if e.count(`SELECT count(*) FROM topic_account_links WHERE tenant_id=$1`, a.id) != 0 {
		t.Fatal("a derived resolution must not be stored")
	}
	// a second company: NOT resolved (not even to the contact's own primary): a person must choose
	xpto := e.account(a.id, "XPTO", "active")
	e.contactLink(a.id, a.contact, xpto, false)
	_, c = e.acctCtx(a.id, admin, topic.ID)
	if c.Status != "needs_choice" || c.Primary != nil || len(c.Candidates) != 2 || c.Candidates[0].AccountID != acme || !c.Candidates[0].ContactPrimary || c.Candidates[1].ContactPrimary {
		t.Fatalf("several companies must ask, never guess (the contact's primary is a preference, not an answer): %+v", c)
	}
	// an archived or ended company is not a candidate
	archived := e.account(a.id, "Velha", "archived")
	e.contactLink(a.id, a.contact, archived, false)
	ended := e.account(a.id, "Encerrada", "active")
	e.contactLink(a.id, a.contact, ended, false)
	e.exec(`UPDATE contact_account_links SET status='ended', ended_at=now() WHERE account_id=$1`, ended)
	if _, c = e.acctCtx(a.id, admin, topic.ID); len(c.Candidates) != 2 {
		t.Fatalf("only active links of non-archived accounts are candidates: %+v", c.Candidates)
	}
	// the topic's primary ticket targeting an account answers it (derived, from a server-validated company)
	tk := e.ticket(a.id, a.conversation, "open")
	e.exec(`UPDATE tickets SET customer_account_id=$2 WHERE id=$1`, tk, xpto)
	e.exec(`INSERT INTO topic_ticket_links(tenant_id,topic_thread_id,ticket_id,relation,created_by) VALUES($1,$2,$3,'primary','agent')`, a.id, topic.ID, tk)
	if _, c = e.acctCtx(a.id, admin, topic.ID); c.Status != "resolved" || c.Primary == nil || c.Primary.AccountID != xpto || c.Primary.Source != "ticket" || c.Primary.Persisted {
		t.Fatalf("ticket-derived: %+v", c)
	}
}

func TestAPersonRecordsTheSubjectsCompanyAndThereIsOnlyOnePrimary(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	topic, _ := e.topicWith(a, admin, "Pedido", "pedido atrasado")
	acme, xpto, third := e.account(a.id, "ACME", "active"), e.account(a.id, "XPTO", "active"), e.account(a.id, "Zeta", "active")
	e.contactLink(a.id, a.contact, acme, true)
	e.contactLink(a.id, a.contact, xpto, false)

	if code := e.linkTopic(a.id, admin, topic.ID, fmt.Sprintf(`{"account_id":%q,"relation":"primary"}`, xpto)); code != 200 {
		t.Fatalf("primary = %d", code)
	}
	_, c := e.acctCtx(a.id, admin, topic.ID)
	if c.Status != "resolved" || c.Primary.AccountID != xpto || c.Primary.Source != "topic_link" || !c.Primary.Persisted || !c.Primary.LinkedToContact {
		t.Fatalf("after choosing: %+v", c)
	}
	// choosing another primary demotes the first to related: never two primaries
	if code := e.linkTopic(a.id, admin, topic.ID, fmt.Sprintf(`{"account_id":%q,"relation":"primary"}`, acme)); code != 200 {
		t.Fatalf("second primary = %d", code)
	}
	if e.count(`SELECT count(*) FROM topic_account_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND relation='primary'`, a.id, topic.ID) != 1 ||
		e.count(`SELECT count(*) FROM topic_account_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND relation='related' AND account_id=$3`, a.id, topic.ID, xpto) != 1 {
		t.Fatal("exactly one primary; the previous one becomes related")
	}
	// a company the contact does not belong to is allowed, but visibly flagged
	if code := e.linkTopic(a.id, admin, topic.ID, fmt.Sprintf(`{"account_id":%q,"relation":"related"}`, third)); code != 200 {
		t.Fatalf("related = %d", code)
	}
	_, c = e.acctCtx(a.id, admin, topic.ID)
	foundThird := false
	for _, r := range c.Related {
		if r.AccountID == third {
			foundThird = true
		}
	}
	if !foundThird || len(c.Related) != 2 {
		t.Fatalf("related: %+v", c.Related)
	}
	// idempotent
	if code := e.linkTopic(a.id, admin, topic.ID, fmt.Sprintf(`{"account_id":%q,"relation":"related"}`, third)); code != 200 || e.count(`SELECT count(*) FROM topic_account_links WHERE tenant_id=$1`, a.id) != 3 {
		t.Fatal("linking the same account again must not duplicate")
	}
	// the conversation itself never carries a company: the context lives on the topic
	if e.count(`SELECT count(*) FROM information_schema.columns WHERE table_name='conversations' AND column_name IN ('account_id','customer_account_id','company_id')`) != 0 {
		t.Fatal("there must be no account on the conversation")
	}
	// removing the primary does not promote anything: back to the derived resolution
	h := e.handler()
	rec := e.call(a.id, admin, http.MethodDelete, "", map[string]string{"topic_id": topic.ID.String(), "account_id": acme.String()}, h.UnlinkAccount)
	if rec.Code != 200 {
		t.Fatalf("unlink = %d", rec.Code)
	}
	_, c = e.acctCtx(a.id, admin, topic.ID)
	if c.Status != "needs_choice" || c.Primary != nil {
		t.Fatalf("after removing the primary the contact's several companies ask again: %+v", c)
	}
	if rec := e.call(a.id, admin, http.MethodDelete, "", map[string]string{"topic_id": topic.ID.String(), "account_id": acme.String()}, h.UnlinkAccount); rec.Code != http.StatusNotFound {
		t.Errorf("unlinking twice = %d", rec.Code)
	}
	for _, act := range []string{"topic.account_linked", "topic.account_unlinked"} {
		if e.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2 AND resource_id=$3`, a.id, act, topic.ID.String()) < 1 {
			t.Errorf("missing audit %s", act)
		}
	}
	// the database refuses a second primary outright
	if _, err := e.seed.Exec(e.ctx, `INSERT INTO topic_account_links(tenant_id,topic_thread_id,account_id,relation) VALUES($1,$2,$3,'primary'),($1,$2,$4,'primary')`, a.id, topic.ID, acme, xpto); err == nil {
		t.Fatal("two primaries must violate the partial unique index")
	}
}

func TestTopicAccountAPIValidationPermissionsAndTenantIsolation(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	adminA, adminB := e.member(a.id, "tenant_admin"), e.member(b.id, "tenant_admin")
	agent := e.member(a.id, "tenant_agent") // account.read + topic.manage, but not the attendant of this conversation
	viewer := e.readOnlyMember(a.id)        // tenant.read only
	topic, _ := e.topicWith(a, adminA, "Pedido", "pedido")
	acme := e.account(a.id, "ACME", "active")
	foreign := e.account(b.id, "De B", "active")
	archived := e.account(a.id, "Arquivada", "archived")

	for name, body := range map[string]string{
		"archived account": fmt.Sprintf(`{"account_id":%q,"relation":"primary"}`, archived),
		"another tenant's": fmt.Sprintf(`{"account_id":%q,"relation":"primary"}`, foreign),
		"unknown account":  fmt.Sprintf(`{"account_id":%q,"relation":"primary"}`, uuid.New()),
		"bad relation":     fmt.Sprintf(`{"account_id":%q,"relation":"exclusive"}`, acme),
		"no account":       `{"relation":"primary"}`,
		"unknown field":    fmt.Sprintf(`{"account_id":%q,"relation":"primary","tenant_id":%q}`, acme, b.id),
	} {
		if code := e.linkTopic(a.id, adminA, topic.ID, body); code < 400 {
			t.Errorf("%s = %d", name, code)
		}
	}
	if e.count(`SELECT count(*) FROM topic_account_links WHERE tenant_id=$1`, a.id) != 0 {
		t.Fatal("a refused request stored something")
	}
	// the agent holds the permissions but does not operate the topic (not assigned, no conversation.manage)
	if code := e.linkTopic(a.id, agent, topic.ID, fmt.Sprintf(`{"account_id":%q,"relation":"primary"}`, acme)); code != http.StatusForbidden {
		t.Errorf("agent who does not operate the topic = %d, want 403", code)
	}
	// a role without topic.read / account.read sees nothing
	if code, _ := e.acctCtx(a.id, viewer, topic.ID); code != http.StatusForbidden {
		t.Errorf("viewer GET = %d", code)
	}
	if code := e.linkTopic(a.id, viewer, topic.ID, fmt.Sprintf(`{"account_id":%q,"relation":"primary"}`, acme)); code != http.StatusForbidden {
		t.Errorf("viewer POST = %d", code)
	}
	// tenant B cannot read or change A's topic: the same 404 as an unknown topic
	if code, _ := e.acctCtx(b.id, adminB, topic.ID); code != http.StatusNotFound {
		t.Errorf("foreign GET = %d", code)
	}
	if code := e.linkTopic(b.id, adminB, topic.ID, fmt.Sprintf(`{"account_id":%q,"relation":"primary"}`, foreign)); code != http.StatusNotFound {
		t.Errorf("foreign POST = %d", code)
	}
	if code, _ := e.acctCtx(b.id, adminB, uuid.New()); code != http.StatusNotFound {
		t.Errorf("unknown topic = %d", code)
	}
}

// Scenario E of the ADR-0018 acceptance list: ONE conversation, one contact with two companies, two subjects. Each subject
// carries its own company; the conversation carries none; a ticket per subject targets its own account.
func TestTwoSubjectsOfOneConversationCarryTheirOwnCompanies(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	acme, xpto := e.account(a.id, "ACME", "active"), e.account(a.id, "XPTO", "active")
	e.contactLink(a.id, a.contact, acme, true)
	e.contactLink(a.id, a.contact, xpto, false)
	one, _ := e.topicWith(a, admin, "Link caiu", "o link da ACME caiu")
	two, _ := e.topicWith(a, admin, "Fatura errada", "a fatura da XPTO veio errada")

	// both subjects start undecided: the contact belongs to two companies, nothing is guessed
	for _, tp := range []uuid.UUID{one.ID, two.ID} {
		if _, c := e.acctCtx(a.id, admin, tp); c.Status != "needs_choice" {
			t.Fatalf("topic %s must ask: %+v", tp, c)
		}
	}
	if code := e.linkTopic(a.id, admin, one.ID, fmt.Sprintf(`{"account_id":%q,"relation":"primary"}`, acme)); code != 200 {
		t.Fatal(code)
	}
	if code := e.linkTopic(a.id, admin, two.ID, fmt.Sprintf(`{"account_id":%q,"relation":"primary"}`, xpto)); code != 200 {
		t.Fatal(code)
	}
	_, c1 := e.acctCtx(a.id, admin, one.ID)
	_, c2 := e.acctCtx(a.id, admin, two.ID)
	if c1.Primary == nil || c1.Primary.AccountID != acme || c2.Primary == nil || c2.Primary.AccountID != xpto {
		t.Fatalf("each subject keeps its own company: %+v / %+v", c1.Primary, c2.Primary)
	}
	// one ticket per subject, each targeting its own account (set server-side from a validated company)
	for _, x := range []struct {
		topic, account uuid.UUID
		subject        string
	}{{one.ID, acme, "Link caiu"}, {two.ID, xpto, "Fatura errada"}} {
		tk := uuid.New()
		e.exec(`INSERT INTO tickets(id,tenant_id,conversation_id,status,subject,topic_scoped,customer_account_id) VALUES($1,$2,$3,'open',$4,true,$5)`, tk, a.id, a.conversation, x.subject, x.account)
		e.exec(`INSERT INTO topic_ticket_links(tenant_id,topic_thread_id,ticket_id,relation,created_by) VALUES($1,$2,$3,'primary','agent')`, a.id, x.topic, tk)
	}
	if e.count(`SELECT count(*) FROM tickets t JOIN topic_ticket_links l ON l.tenant_id=t.tenant_id AND l.ticket_id=t.id
	            WHERE t.tenant_id=$1 AND ((l.topic_thread_id=$2 AND t.customer_account_id=$4) OR (l.topic_thread_id=$3 AND t.customer_account_id=$5))`, a.id, one.ID, two.ID, acme, xpto) != 2 {
		t.Fatal("each subject's ticket targets its own account")
	}
	if e.count(`SELECT count(*) FROM information_schema.columns WHERE table_name='conversations' AND column_name LIKE '%account%'`) != 0 {
		t.Fatal("the conversation carries no company")
	}
}
