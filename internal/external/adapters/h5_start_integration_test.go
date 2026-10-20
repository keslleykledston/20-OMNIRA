package adapters_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/google/uuid"
)

// ADR-0050: POST /api/v1/external/conversations — create-or-reuse a conversation by phone. Real PostgreSQL as omnira_app, the real HTTP
// handlers, the real control service and sender.

var startScopes = append(append([]string{}, journalScopes...), "contact.write", "conversation.open")

type startResp struct {
	Code int
	Raw  []byte
	Doc  struct {
		ConversationID uuid.UUID `json:"conversation_id"`
		ContactID      uuid.UUID `json:"contact_id"`
		Created        bool      `json:"created"`
		Status         string    `json:"status"`
		Control        struct {
			State   string `json:"state"`
			Version int64  `json:"version"`
			Holder  *struct {
				Kind string `json:"kind"`
				Mine bool   `json:"mine"`
			} `json:"holder"`
		} `json:"control"`
		MessageID *uuid.UUID `json:"message_id"`
		Delivery  string     `json:"delivery"`
		Code      string     `json:"code"`
	}
}

func (e *env) start(token, key string, body any) startResp {
	e.t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/external/conversations", bytes.NewReader(raw))
	e.seq++
	req.RemoteAddr = "198.51.100." + strconv.Itoa(e.seq%250+1) + ":" + strconv.Itoa(4000+e.seq/250)
	if e.addr != "" {
		req.RemoteAddr = e.addr
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	e.ext.ServeHTTP(rec, req)
	r := startResp{Code: rec.Code, Raw: rec.Body.Bytes()}
	_ = json.Unmarshal(r.Raw, &r.Doc)
	return r
}

// openConversationForPhone seeds a pre-existing OPEN conversation for a known phone, control free (nobody holds it) — the fixture for the
// "reuse" half of ADR-0050, which this command must never auto-claim.
func (e *env) openConversationForPhone(tenant, connID uuid.UUID, phone string) (convID, contactID uuid.UUID) {
	e.t.Helper()
	contactID = uuid.New()
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164,status) VALUES($1,$2,$3,$4,'active')`, contactID, tenant, phone, phone)
	convID = uuid.New()
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,status,created_at,updated_at) VALUES($1,$2,$3,$4,'open',now(),now())`, convID, tenant, contactID, connID)
	return
}

// The headline case: no conversation exists for this phone yet, so the call creates one, auto-claims it (there was no prior holder to
// take from) and, because a template was given, sends it in the SAME call even with no prior inbound message (outside the 24h window,
// which only a template may cross). A retry with the same Idempotency-Key must return byte-for-byte the same conversation and message,
// never a second row of either.
func TestWriteStartConversationCreatesHeldConversationAndSendsTemplateOutsideWindow(t *testing.T) {
	e := newEnv(t)
	e.writeAPI()
	tn := e.tenant()
	admin := e.member(tn, "tenant_admin")
	app := e.install(admin, tn, "Gateway", startScopes...)
	tok := app.Credential.Token

	meta := uuid.New()
	e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','meta_cloud','official',$3,'active','["text"]'::jsonb)`, meta, tn, "m-"+meta.String())
	tpl := uuid.New()
	e.exec(`INSERT INTO channel_message_templates(id,tenant_id,connection_id,name,language,category,status,body_text,variable_count,sendable) VALUES($1,$2,$3,'alerta','pt_BR','UTILITY','APPROVED','Alerta: {{1}}.',1,true)`, tpl, tn, meta)

	phone := "+5592999990001"
	r := e.start(tok, "key-start-00001", map[string]any{
		"phone_e164": phone, "channel_id": meta.String(),
		"message": map[string]any{"template": map[string]any{"id": tpl, "params": []string{"CPU 95%"}}},
	})
	if r.Code != http.StatusCreated || !r.Doc.Created {
		t.Fatalf("start = %d %s", r.Code, r.Raw)
	}
	if r.Doc.Control.State != "held" || r.Doc.Control.Holder == nil || !r.Doc.Control.Holder.Mine {
		t.Fatalf("a brand-new conversation must be held by the caller: %+v", r.Doc.Control)
	}
	if r.Doc.MessageID == nil || r.Doc.Delivery != "queued" {
		t.Fatalf("the template message must be queued in the same call: %+v", r.Doc)
	}
	if n := e.count(`SELECT count(*) FROM conversations WHERE tenant_id=$1 AND contact_id=$2`, tn, r.Doc.ContactID); n != 1 {
		t.Fatalf("exactly one conversation must exist for this contact, got %d", n)
	}

	for i := 0; i < 3; i++ {
		again := e.start(tok, "key-start-00001", map[string]any{
			"phone_e164": phone, "channel_id": meta.String(),
			"message": map[string]any{"template": map[string]any{"id": tpl, "params": []string{"CPU 95%"}}},
		})
		if again.Code != http.StatusCreated || again.Doc.ConversationID != r.Doc.ConversationID || again.Doc.MessageID == nil || *again.Doc.MessageID != *r.Doc.MessageID {
			t.Fatalf("replay %d must return the SAME conversation/message: %d %s", i, again.Code, again.Raw)
		}
	}
	if n := e.count(`SELECT count(*) FROM conversations WHERE tenant_id=$1 AND contact_id=$2`, tn, r.Doc.ContactID); n != 1 {
		t.Fatalf("a replay must never create a second conversation, got %d", n)
	}
	if n := e.count(`SELECT count(*) FROM outbox_events WHERE aggregate_id=$1`, r.Doc.MessageID.String()); n != 1 {
		t.Fatalf("exactly one outbox job for the template send, got %d", n)
	}

	// channel_id omitted, exactly one active text channel for the tenant: still resolves
	r2 := e.start(tok, "key-start-00002", map[string]any{"phone_e164": "+5592999990002"})
	if r2.Code != http.StatusCreated || r2.Doc.MessageID != nil {
		t.Fatalf("start without a message = %d %s", r2.Code, r2.Raw)
	}
}

// ADR-0050's resolved "Questão em aberto nº3": auto-claim happens ONLY when this call CREATES the conversation. Reusing one that is FREE
// never claims it (and so can never send through it without a separate claim); reusing one held by ANOTHER party is 409 held_by_other,
// never a duplicate; reusing one this SAME installation already holds works exactly like a normal send.
func TestWriteStartConversationReuseSemanticsNeverAutoClaimsAFreeOrForeignHeldConversation(t *testing.T) {
	e := newEnv(t)
	e.writeAPI()
	tn := e.tenant()
	admin := e.member(tn, "tenant_admin")
	appA := e.install(admin, tn, "Gateway A", startScopes...)
	appB := e.install(admin, tn, "Workspace App B", append(append([]string{}, startScopes...), "conversation.claim")...)
	conn := e.connection(tn)

	phoneFree := "+5592999990011"
	convFree, contactFree := e.openConversationForPhone(tn, conn, phoneFree)
	rFree := e.start(appA.Credential.Token, "key-reuse-free-01", map[string]any{"phone_e164": phoneFree})
	if rFree.Code != http.StatusOK || rFree.Doc.Created {
		t.Fatalf("reuse of a free conversation must be 200/created=false: %d %s", rFree.Code, rFree.Raw)
	}
	if rFree.Doc.ConversationID != convFree || rFree.Doc.ContactID != contactFree {
		t.Fatalf("reuse must return the SAME conversation/contact: %+v", rFree.Doc)
	}
	if rFree.Doc.Control.State != "unassigned" {
		t.Fatalf("reusing a free conversation must never auto-claim it: %+v", rFree.Doc.Control)
	}
	rFreeMsg := e.start(appA.Credential.Token, "key-reuse-free-msg", map[string]any{"phone_e164": phoneFree, "message": map[string]any{"text": "oi"}})
	if rFreeMsg.Code != http.StatusConflict || rFreeMsg.Doc.Code != "not_holder" {
		t.Fatalf("sending through a free, un-held reuse = %d %s, want 409 not_holder", rFreeMsg.Code, rFreeMsg.Raw)
	}

	phoneHeld := "+5592999990012"
	convHeld, _ := e.openConversationForPhone(tn, conn, phoneHeld)
	if c := e.cmd(appB.Credential.Token, convHeld, "claim", "key-claimB-0001", "", nil); c.Code != http.StatusOK {
		t.Fatalf("appB claim = %d %s", c.Code, c.Raw)
	}
	rHeld := e.start(appA.Credential.Token, "key-reuse-held-01", map[string]any{"phone_e164": phoneHeld})
	if rHeld.Code != http.StatusConflict || rHeld.Doc.Code != "held_by_other" {
		t.Fatalf("reuse of a conversation held by another = %d %s, want 409 held_by_other", rHeld.Code, rHeld.Raw)
	}
	if n := e.count(`SELECT count(*) FROM conversations WHERE tenant_id=$1 AND id=$2`, tn, convHeld); n != 1 {
		t.Fatalf("held_by_other must never create a duplicate conversation")
	}

	phoneMine := "+5592999990013"
	convMine, _ := e.openConversationForPhone(tn, conn, phoneMine)
	if c := e.cmd(appA.Credential.Token, convMine, "claim", "key-claimA-0001", "", nil); c.Code != http.StatusOK {
		t.Fatalf("appA claim = %d %s", c.Code, c.Raw)
	}
	rMine := e.start(appA.Credential.Token, "key-reuse-mine-01", map[string]any{"phone_e164": phoneMine, "message": map[string]any{"text": "oi, tudo bem?"}})
	if rMine.Code != http.StatusOK || rMine.Doc.Created || rMine.Doc.MessageID == nil {
		t.Fatalf("reuse of a conversation this installation already holds must send = %d %s", rMine.Code, rMine.Raw)
	}
}

// Missing scope, a channel outside the installation's allow-list, an ambiguous auto-pick, and tenant isolation (a phone already used in
// tenant A never surfaces — nor can its channel_id be borrowed — by an installation of tenant B).
func TestWriteStartConversationScopeChannelAndTenancyGuards(t *testing.T) {
	e := newEnv(t)
	e.writeAPI()
	tn, tb := e.tenant(), e.tenant()
	admin := e.member(tn, "tenant_admin")
	adminB := e.member(tb, "tenant_admin")

	full := e.install(admin, tn, "Gateway", startScopes...)
	noScope := e.install(admin, tn, "sem escopo", journalScopes...)
	connA1, connA2 := e.connection(tn), e.connection(tn)
	appB := e.install(adminB, tb, "Gateway B", startScopes...)

	phone := "+5592999990021"
	if r := e.start(noScope.Credential.Token, "key-noscope-01", map[string]any{"phone_e164": phone}); r.Code != http.StatusForbidden {
		t.Errorf("without contact.write/conversation.open = %d, want 403: %s", r.Code, r.Raw)
	}

	code, body := e.admin(admin, tn, http.MethodPost, "", map[string]any{"name": "limited", "scopes": startScopes, "channel_ids": []string{connA1.String()}})
	if code != http.StatusCreated {
		t.Fatalf("create limited = %d %s", code, body)
	}
	var limited created
	_ = json.Unmarshal(body, &limited)
	if r := e.start(limited.Credential.Token, "key-limited-01", map[string]any{"phone_e164": phone, "channel_id": connA2.String()}); r.Code != http.StatusForbidden || r.Doc.Code != "channel_not_allowed" {
		t.Errorf("channel outside the allow-list = %d %s, want 403 channel_not_allowed", r.Code, r.Raw)
	}

	if r := e.start(full.Credential.Token, "key-ambig-01", map[string]any{"phone_e164": phone}); r.Code != http.StatusUnprocessableEntity || r.Doc.Code != "channel_ambiguous" {
		t.Errorf("two active channels, none named = %d %s, want 422 channel_ambiguous", r.Code, r.Raw)
	}

	if r := e.start(appB.Credential.Token, "key-tenantb-01", map[string]any{"phone_e164": phone, "channel_id": connA1.String()}); r.Code != http.StatusConflict || r.Doc.Code != "channel_unavailable" {
		t.Errorf("tenant B naming tenant A's channel_id = %d %s, want 409 channel_unavailable", r.Code, r.Raw)
	}
	connB := e.connection(tb)
	rB := e.start(appB.Credential.Token, "key-tenantb-02", map[string]any{"phone_e164": phone, "channel_id": connB.String()})
	if rB.Code != http.StatusCreated || !rB.Doc.Created {
		t.Fatalf("tenant B must create its OWN conversation for a phone used in tenant A, never reuse A's: %d %s", rB.Code, rB.Raw)
	}
	if n := e.count(`SELECT count(*) FROM conversations WHERE tenant_id=$1`, tb); n != 1 {
		t.Fatalf("tenant B conversation count = %d, want 1", n)
	}
}
