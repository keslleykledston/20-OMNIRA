package adapters_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	attendanceadapters "github.com/omnira/omnira/internal/attendance/adapters"
	attendanceapp "github.com/omnira/omnira/internal/attendance/application"
	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	"github.com/omnira/omnira/internal/conversationcontrol"
	"github.com/omnira/omnira/internal/external/adapters"
	"github.com/omnira/omnira/internal/external/domain"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapp "github.com/omnira/omnira/internal/messages/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	routingadapters "github.com/omnira/omnira/internal/routing/adapters"
)

// ADR-0046 H5: the coordinated write commands. Real PostgreSQL as omnira_app, the real HTTP handlers, the real control service, sender and attendance service.

var writeScopes = append(append([]string{}, journalScopes...), "conversation.claim", "conversation.transfer", "conversation.finalize", "message.send")

func (e *env) writeAPI() {
	e.readAPI(nil)
	e.eventsAPI()
	mw := adapters.NewMiddleware(adapters.NewPostgresLookup(e.app))
	att := attendanceapp.NewService(attendanceadapters.NewPostgresRepository(e.app), nil, nil)
	deps := adapters.WriteDeps{
		Sender: messagesapp.NewExternalSender(messagesadapters.NewPostgresOutboundStore(e.app)),
		Finalize: func(ctx context.Context, inst, conv uuid.UUID, reason, note string) (adapters.FinalizeOutcome, error) {
			res, err := att.FinalizeByExternalApp(ctx, inst, conv, attendancedomain.CloseReason(reason), note)
			if err != nil {
				return adapters.FinalizeOutcome{}, err
			}
			out := adapters.FinalizeOutcome{Reason: reason}
			if res.Closure != nil {
				out.ClosureID, out.Reason = res.Closure.ID, string(res.Closure.Reason)
			}
			return out, nil
		},
		Eligible:  adapters.SectorsEligible,
		QueueOpen: adapters.SectorsQueueOpen,
	}
	w := adapters.NewWriteHandler(e.app, deps)
	post := func(op string, scope domain.Scope, h http.Handler) {
		e.ext.Handle("POST /api/v1/external/conversations/{id}/"+op, mw.Authenticated(adapters.RequireScope(scope, h)))
	}
	post("claim", domain.ScopeConversationClaim, w.Claim())
	post("release", domain.ScopeConversationClaim, w.Release())
	post("transfer", domain.ScopeConversationTransfer, w.Transfer())
	post("messages", domain.ScopeMessageSend, w.Message())
	post("finalize", domain.ScopeConversationFinalize, w.Finalize())
}

type cmdResp struct {
	Code   int
	Header http.Header
	Raw    []byte
	Doc    struct {
		ConversationID uuid.UUID `json:"conversation_id"`
		Status         string    `json:"status"`
		Control        struct {
			State   string `json:"state"`
			Version int64  `json:"version"`
			Holder  *struct {
				Kind string `json:"kind"`
				Mine bool   `json:"mine"`
			} `json:"holder"`
		} `json:"control"`
		Changed   *bool      `json:"changed"`
		MessageID *uuid.UUID `json:"message_id"`
		Delivery  string     `json:"delivery"`
		ClosureID *uuid.UUID `json:"closure_id"`
		Code      string     `json:"code"`
	}
}

func (e *env) cmd(token string, conv uuid.UUID, op, key, ifMatch string, body any) cmdResp {
	e.t.Helper()
	var raw []byte
	switch b := body.(type) {
	case nil:
	case string:
		raw = []byte(b)
	default:
		raw, _ = json.Marshal(b)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/external/conversations/"+conv.String()+"/"+op, bytes.NewReader(raw))
	e.seq++
	req.RemoteAddr = "198.51.100." + strconv.Itoa(e.seq%250+1) + ":" + strconv.Itoa(4000+e.seq/250)
	if e.addr != "" {
		req.RemoteAddr = e.addr
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if len(raw) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	e.ext.ServeHTTP(rec, req)
	r := cmdResp{Code: rec.Code, Header: rec.Header(), Raw: rec.Body.Bytes()}
	_ = json.Unmarshal(r.Raw, &r.Doc)
	return r
}

func match(v int64) string { return `"control-` + strconv.FormatInt(v, 10) + `"` }

func (e *env) writeEnv() (tenant, admin uuid.UUID, app created, conv uuid.UUID, conn uuid.UUID) {
	e.t.Helper()
	e.writeAPI()
	tenant = e.tenant()
	admin = e.member(tenant, "tenant_admin")
	code, body := e.admin(admin, tenant, http.MethodPost, "", map[string]any{"name": "CRM", "scopes": writeScopes, "rate_limit_per_min": 6000})
	if code != http.StatusCreated {
		e.t.Fatalf("create = %d %s", code, body)
	}
	_ = json.Unmarshal(body, &app)
	conn = e.connection(tenant)
	conv, _ = e.conv(tenant, convOpts{conn: &conn})
	e.message(tenant, conv, "inbound", "oi", "received", time.Minute, nil)
	return
}

// agentInQueue makes a person who can receive a conversation of the queue by the router's own rule: an active member with an active agent profile.
func (e *env) agentInQueue(tenant, queue uuid.UUID) uuid.UUID {
	u := e.member(tenant, "tenant_agent")
	e.exec(`INSERT INTO agent_profiles(id,tenant_id,membership_id,status) SELECT gen_random_uuid(), $1, m.id, 'active' FROM memberships m WHERE m.tenant_id=$1 AND m.user_id=$2`, tenant, u)
	e.exec(`INSERT INTO queue_members(tenant_id,queue_id,user_id,active,available,capacity) VALUES($1,$2,$3,true,true,50)`, tenant, queue, u)
	return u
}

func (e *env) count(q string, a ...any) int {
	var n int
	if err := e.seed.QueryRow(e.ctx, q, a...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// claim and release: the Idempotency-Key, If-Match and the claim exception.
func TestWriteClaimReleaseIdempotencyAndPreconditions(t *testing.T) {
	e := newEnv(t)
	tn, _, app, conv, _ := e.writeEnv()
	tok := app.Credential.Token

	// the key is mandatory and well formed
	if r := e.cmd(tok, conv, "claim", "", "", nil); r.Code != http.StatusBadRequest {
		t.Errorf("no Idempotency-Key = %d, want 400", r.Code)
	}
	if r := e.cmd(tok, conv, "claim", "short", "", nil); r.Code != http.StatusBadRequest {
		t.Errorf("short key = %d, want 400", r.Code)
	}
	if r := e.cmd(tok, conv, "claim", "key-claim-0001", "bad", nil); r.Code != http.StatusBadRequest {
		t.Errorf("malformed If-Match = %d, want 400", r.Code)
	}

	// claim of a free conversation needs no If-Match
	first := e.cmd(tok, conv, "claim", "key-claim-0001", "", nil)
	if first.Code != 200 || first.Doc.Changed == nil || !*first.Doc.Changed || first.Doc.Control.Holder == nil || !first.Doc.Control.Holder.Mine ||
		first.Doc.Control.Holder.Kind != "crm_application" || first.Header.Get("ETag") != match(first.Doc.Control.Version) {
		t.Fatalf("claim = %d %s", first.Code, first.Raw)
	}
	v := first.Doc.Control.Version
	// the same key, the same payload: the same answer, marked as a replay, and nothing happens twice
	again := e.cmd(tok, conv, "claim", "key-claim-0001", "", "{}")
	if again.Code != 200 || again.Header.Get("Idempotent-Replay") != "true" || again.Doc.Control.Version != v || again.Doc.Changed == nil || !*again.Doc.Changed {
		t.Fatalf("replay = %d %s", again.Code, again.Raw)
	}
	if n := e.count(`SELECT count(*) FROM conversation_control_events WHERE conversation_id=$1 AND reason='crm_claim'`, conv); n != 1 {
		t.Errorf("claim recorded %d times", n)
	}
	// the same key for ANOTHER conversation is a different request
	other, _ := e.conv(tn, convOpts{})
	if r := e.cmd(tok, other, "claim", "key-claim-0001", "", nil); r.Code != 422 || r.Doc.Code != "idempotency_key_reused" {
		t.Errorf("key reused for another conversation = %d %s", r.Code, r.Raw)
	}
	// the claim exception: a NEW key, no If-Match, already the holder: nothing changes
	rep := e.cmd(tok, conv, "claim", "key-claim-0002", "", nil)
	if rep.Code != 200 || rep.Doc.Changed == nil || *rep.Doc.Changed || rep.Doc.Control.Version != v || rep.Header.Get("Idempotent-Replay") != "" {
		t.Errorf("repeated claim by the holder = %d %s", rep.Code, rep.Raw)
	}
	// If-Match on a claim, when sent, must be the current version
	if r := e.cmd(tok, conv, "claim", "key-claim-0003", match(v+5), nil); r.Code != 412 || r.Header.Get("ETag") != match(v) {
		t.Errorf("claim with a stale If-Match = %d etag=%s", r.Code, r.Header.Get("ETag"))
	}
	if r := e.cmd(tok, conv, "claim", "key-claim-0004", match(v), nil); r.Code != 200 {
		t.Errorf("claim with the current If-Match = %d", r.Code)
	}

	// release: If-Match is required, stale is 412, the right one frees it
	if r := e.cmd(tok, conv, "release", "key-release-01", "", nil); r.Code != 428 || r.Doc.Code != "if_match_required" {
		t.Errorf("release without If-Match = %d %s", r.Code, r.Raw)
	}
	if r := e.cmd(tok, conv, "release", "key-release-01", match(v-1), nil); r.Code != 412 {
		t.Errorf("release with a stale If-Match = %d", r.Code)
	}
	rel := e.cmd(tok, conv, "release", "key-release-01", match(v), nil)
	if rel.Code != 200 || rel.Doc.Control.State != "unassigned" || rel.Doc.Control.Version != v+1 {
		t.Fatalf("release = %d %s", rel.Code, rel.Raw)
	}
	// a retry after the commit (the response was lost): the stored answer, not a 412, not a second release
	if r := e.cmd(tok, conv, "release", "key-release-01", match(v), nil); r.Code != 200 || r.Header.Get("Idempotent-Replay") != "true" || r.Doc.Control.Version != v+1 {
		t.Errorf("retry of a committed release = %d %s", r.Code, r.Raw)
	}
	// a NEW release of what it no longer holds
	if r := e.cmd(tok, conv, "release", "key-release-02", match(v+1), nil); r.Code != 409 || r.Doc.Code != "not_holder" {
		t.Errorf("release of a free conversation = %d %s", r.Code, r.Raw)
	}
	// no receipt for a refusal
	if n := e.count(`SELECT count(*) FROM external_command_receipts WHERE conversation_id=$1`, conv); n != 4 { // claim 1,2,4 and release 1
		t.Errorf("receipts = %d, want 4 (refusals leave none)", n)
	}
}

// Sending: the hold and the version are proved, the message is the platform's own queued message, 202 means queued, a retry yields the same message.
func TestWriteMessageQueuedOnceWithTheSameIdAndPreconditions(t *testing.T) {
	e := newEnv(t)
	tn, _, app, conv, _ := e.writeEnv()
	tok := app.Credential.Token
	text := map[string]any{"text": "Olá, aqui é o CRM"}

	if r := e.cmd(tok, conv, "messages", "key-msg-00001", match(1), text); r.Code != 409 || r.Doc.Code != "not_holder" {
		t.Fatalf("send without holding = %d %s", r.Code, r.Raw)
	}
	claim := e.cmd(tok, conv, "claim", "key-claim-0001", "", nil)
	v := claim.Doc.Control.Version
	if r := e.cmd(tok, conv, "messages", "key-msg-00001", "", text); r.Code != 428 {
		t.Errorf("send without If-Match = %d", r.Code)
	}
	if r := e.cmd(tok, conv, "messages", "key-msg-00001", match(v+9), text); r.Code != 412 || r.Doc.Code != "stale_control_version" {
		t.Errorf("send with a stale If-Match = %d %s", r.Code, r.Raw)
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, conv); n != 0 {
		t.Fatalf("a refused send queued %d messages", n)
	}
	sent := e.cmd(tok, conv, "messages", "key-msg-00001", match(v), text)
	if sent.Code != 202 || sent.Doc.Delivery != "queued" || sent.Doc.MessageID == nil || sent.Header.Get("ETag") != match(v) {
		t.Fatalf("send = %d %s", sent.Code, sent.Raw)
	}
	mid := *sent.Doc.MessageID
	// what was written: the application's own message, no person, no key; one delivery job that names only the message
	var byUser, byInst *uuid.UUID
	var idem *string
	var status, body string
	if err := e.seed.QueryRow(e.ctx, `SELECT sent_by_user_id, sent_by_installation_id, idempotency_key, status, body FROM messages WHERE id=$1`, mid).Scan(&byUser, &byInst, &idem, &status, &body); err != nil {
		t.Fatal(err)
	}
	if byUser != nil || byInst == nil || *byInst != app.Installation.ID || idem != nil || status != "queued" || body != "Olá, aqui é o CRM" {
		t.Errorf("message row: user=%v installation=%v key=%v status=%s", byUser, byInst, idem, status)
	}
	var payload string
	if n := e.count(`SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type='job.channel.send_text.v1' AND aggregate_id=$2`, tn, mid.String()); n != 1 {
		t.Errorf("delivery jobs = %d, want 1", n)
	}
	_ = e.seed.QueryRow(e.ctx, `SELECT payload::text FROM outbox_events WHERE aggregate_id=$1`, mid.String()).Scan(&payload)
	if strings.Contains(payload, "Olá") || strings.Contains(payload, "+55") {
		t.Errorf("the job carries content: %s", payload)
	}
	// the response was lost: retrying with the same key returns the same message and queues nothing new
	for i := 0; i < 3; i++ {
		r := e.cmd(tok, conv, "messages", "key-msg-00001", match(v), `{ "text" : "Olá, aqui é o CRM" }`) // whitespace differs, the payload is the same
		if r.Code != 202 || r.Doc.MessageID == nil || *r.Doc.MessageID != mid || r.Header.Get("Idempotent-Replay") != "true" {
			t.Fatalf("retry %d = %d %s", i, r.Code, r.Raw)
		}
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, conv); n != 1 {
		t.Errorf("outbound messages = %d, want 1", n)
	}
	if r := e.cmd(tok, conv, "messages", "key-msg-00001", match(v), map[string]any{"text": "outro texto"}); r.Code != 422 || r.Doc.Code != "idempotency_key_reused" {
		t.Errorf("same key, other text = %d %s", r.Code, r.Raw)
	}
	// shape and size
	for name, body := range map[string]any{"empty": map[string]any{"text": "  "}, "both": map[string]any{"text": "a", "template": map[string]any{"id": uuid.New()}}, "neither": map[string]any{},
		"unknown field": map[string]any{"text": "a", "tenant_id": tn}, "too long": map[string]any{"text": strings.Repeat("a", 4097)}, "not json": "texto solto"} {
		r := e.cmd(tok, conv, "messages", "key-shape-"+strings.ReplaceAll(name, " ", "-"), match(v), body)
		if r.Code != 422 && r.Code != 415 {
			t.Errorf("%s = %d %s, want 422/415", name, r.Code, r.Raw)
		}
	}
	if r := e.cmd(tok, conv, "messages", "key-shape-huge-1", match(v), `{"text":"`+strings.Repeat("a", 70000)+`"}`); r.Code != 400 {
		t.Errorf("a 70 kB body = %d, want 400", r.Code)
	}
	// the events say it was the application (so it can recognise its own echo and still follow the next statuses)
	e.sequence()
	var origin string
	var mine bool
	for _, ev := range e.allEvents(app.Credential.Token, 0) {
		if ev.Type == "message.created.v1" && ev.Aggregate.ID == mid {
			origin, mine = ev.Origin.Kind, ev.Origin.Mine
		}
	}
	if origin != "crm" || !mine {
		t.Errorf("message.created origin=%q mine=%v", origin, mine)
	}
	// and the audit row names the application, not a person
	var actor *uuid.UUID
	var meta string
	if err := e.seed.QueryRow(e.ctx, `SELECT actor_id, metadata::text FROM audit_events WHERE tenant_id=$1 AND action='external_app.command' AND metadata->>'operation'='message'`, tn).Scan(&actor, &meta); err != nil {
		t.Fatal(err)
	}
	if actor != nil || !strings.Contains(meta, `"actor_kind": "crm_application"`) || strings.Contains(meta, "Olá") {
		t.Errorf("audit: actor=%v meta=%s", actor, meta)
	}
}

// The WhatsApp rules are the platform's: outside the 24 h window only a template, channel and closed-conversation refusals, nothing queued.
func TestWriteMessageKeepsTheWindowTheChannelAndTheClosedRules(t *testing.T) {
	e := newEnv(t)
	e.writeAPI()
	tn := e.tenant()
	admin := e.member(tn, "tenant_admin")
	app := e.install(admin, tn, "CRM", writeScopes...)
	tok := app.Credential.Token
	meta := uuid.New()
	e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','meta_cloud','official',$3,'active','["text"]'::jsonb)`, meta, tn, "m-"+meta.String())
	convOld, _ := e.conv(tn, convOpts{conn: &meta})
	e.message(tn, convOld, "inbound", "faz tempo", "received", 30*time.Hour, nil)
	v := e.cmd(tok, convOld, "claim", "key-claim-0001", "", nil).Doc.Control.Version
	r := e.cmd(tok, convOld, "messages", "key-msg-00001", match(v), map[string]any{"text": "olá"})
	if r.Code != 422 || r.Doc.Code != "window_closed" {
		t.Fatalf("free text outside the window = %d %s", r.Code, r.Raw)
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, convOld); n != 0 {
		t.Fatalf("a refused send queued a message")
	}
	// an approved template goes out, inside or outside the window; one that is not approved or belongs to another line does not
	tpl, pending := uuid.New(), uuid.New()
	e.exec(`INSERT INTO channel_message_templates(id,tenant_id,connection_id,name,language,category,status,body_text,variable_count,sendable) VALUES($1,$2,$3,'boas_vindas','pt_BR','UTILITY','APPROVED','Olá {{1}}.',1,true)`, tpl, tn, meta)
	e.exec(`INSERT INTO channel_message_templates(id,tenant_id,connection_id,name,language,category,status,body_text,variable_count,sendable) VALUES($1,$2,$3,'pendente','pt_BR','UTILITY','PENDING','x',0,true)`, pending, tn, meta)
	if r := e.cmd(tok, convOld, "messages", "key-tpl-000001", match(v), map[string]any{"template": map[string]any{"id": pending}}); r.Code != 422 || r.Doc.Code != "invalid_template" {
		t.Errorf("a template that is not approved = %d %s", r.Code, r.Raw)
	}
	if r := e.cmd(tok, convOld, "messages", "key-tpl-000002", match(v), map[string]any{"template": map[string]any{"id": tpl, "params": []string{}}}); r.Code != 422 {
		t.Errorf("a template with missing variables = %d %s", r.Code, r.Raw)
	}
	ok := e.cmd(tok, convOld, "messages", "key-tpl-000003", match(v), map[string]any{"template": map[string]any{"id": tpl, "params": []string{"Maria"}}})
	if ok.Code != 202 || ok.Doc.MessageID == nil {
		t.Fatalf("approved template = %d %s", ok.Code, ok.Raw)
	}
	var tname string
	var params string
	if err := e.seed.QueryRow(e.ctx, `SELECT template_name, params::text FROM message_template_sends WHERE message_id=$1`, *ok.Doc.MessageID).Scan(&tname, &params); err != nil || tname != "boas_vindas" || !strings.Contains(params, "Maria") {
		t.Errorf("template record: %s %s (%v)", tname, params, err)
	}
	// a line that does not use templates (WAHA) refuses one
	waha := e.connection(tn)
	convW, _ := e.conv(tn, convOpts{conn: &waha})
	vw := e.cmd(tok, convW, "claim", "key-claim-0002", "", nil).Doc.Control.Version
	if r := e.cmd(tok, convW, "messages", "key-tpl-000004", match(vw), map[string]any{"template": map[string]any{"id": tpl}}); r.Code != 422 {
		t.Errorf("template on a line without templates = %d %s", r.Code, r.Raw)
	}
	// no channel
	convN, _ := e.conv(tn, convOpts{})
	vn := e.cmd(tok, convN, "claim", "key-claim-0003", "", nil).Doc.Control.Version
	if r := e.cmd(tok, convN, "messages", "key-msg-00002", match(vn), map[string]any{"text": "x"}); r.Code != 409 || r.Doc.Code != "channel_unavailable" {
		t.Errorf("no channel = %d %s", r.Code, r.Raw)
	}
	// closed
	fin := e.cmd(tok, convW, "finalize", "key-fin-000001", match(vw), map[string]any{"reason": "resolved"})
	if fin.Code != 200 {
		t.Fatalf("finalize = %d %s", fin.Code, fin.Raw)
	}
	if r := e.cmd(tok, convW, "messages", "key-msg-00003", match(fin.Doc.Control.Version), map[string]any{"text": "x"}); r.Code != 409 || r.Doc.Code != "conversation_closed" {
		t.Errorf("send to a closed conversation = %d %s", r.Code, r.Raw)
	}
}

// Transfer: to a queue (the router decides) or to one eligible person; an ineligible person, a foreign queue or a stale version change nothing.
func TestWriteTransferToQueueAndToAnEligiblePerson(t *testing.T) {
	e := newEnv(t)
	tn, _, app, conv, _ := e.writeEnv()
	tok := app.Credential.Token
	other := e.tenant()
	foreignQueue := e.queue(other)
	q1 := e.queue(tn)
	agent, outsider := e.agentInQueue(tn, q1), e.member(tn, "tenant_agent")
	v := e.cmd(tok, conv, "claim", "key-claim-0001", "", nil).Doc.Control.Version

	if r := e.cmd(tok, conv, "transfer", "key-trf-000001", "", map[string]any{"queue_id": q1}); r.Code != 428 {
		t.Errorf("transfer without If-Match = %d", r.Code)
	}
	if r := e.cmd(tok, conv, "transfer", "key-trf-000001", match(v+3), map[string]any{"queue_id": q1}); r.Code != 412 {
		t.Errorf("transfer with a stale version = %d", r.Code)
	}
	for name, body := range map[string]map[string]any{"a queue of another company": {"queue_id": foreignQueue}, "a queue that does not exist": {"queue_id": uuid.New()},
		"a person who is not in the queue": {"queue_id": q1, "user_id": outsider}, "no queue": {"user_id": agent}} {
		r := e.cmd(tok, conv, "transfer", "key-trf-"+strings.ReplaceAll(name, " ", "-"), match(v), body)
		if r.Code != 422 {
			t.Errorf("%s = %d %s, want 422", name, r.Code, r.Raw)
		}
	}
	var kind string
	var ver int64
	_ = e.seed.QueryRow(e.ctx, `SELECT control_kind, control_version FROM conversations WHERE id=$1`, conv).Scan(&kind, &ver)
	if kind != "crm_application" || ver != v {
		t.Fatalf("a refused transfer changed the conversation: %s v%d", kind, ver)
	}
	// to a person who is eligible
	to := e.cmd(tok, conv, "transfer", "key-trf-000002", match(v), map[string]any{"queue_id": q1, "user_id": agent})
	if to.Code != 200 || to.Doc.Control.Holder == nil || to.Doc.Control.Holder.Kind != "tenant_user" || to.Doc.Control.Holder.Mine {
		t.Fatalf("transfer to a person = %d %s", to.Code, to.Raw)
	}
	var assigned *uuid.UUID
	var queue *uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT assigned_to_user_id, queue_id FROM conversations WHERE id=$1`, conv).Scan(&assigned, &queue)
	if assigned == nil || *assigned != agent || queue == nil || *queue != q1 {
		t.Errorf("assigned=%v queue=%v", assigned, queue)
	}
	// the application no longer holds it: every command is refused, and the person's name never reached the answer
	if strings.Contains(string(to.Raw), agent.String()) {
		t.Errorf("the answer names the person: %s", to.Raw)
	}
	if r := e.cmd(tok, conv, "messages", "key-msg-00001", match(to.Doc.Control.Version), map[string]any{"text": "x"}); r.Code != 409 || r.Doc.Code != "held_by_other" {
		t.Errorf("send after the transfer = %d %s", r.Code, r.Raw)
	}
	// to a queue only: the application lets go and the router looks at it
	conv2, _ := e.conv(tn, convOpts{})
	v2 := e.cmd(tok, conv2, "claim", "key-claim-0002", "", nil).Doc.Control.Version
	tq := e.cmd(tok, conv2, "transfer", "key-trf-000003", match(v2), map[string]any{"queue_id": q1})
	if tq.Code != 200 || tq.Doc.Control.State != "unassigned" || tq.Doc.Control.Version != v2+1 {
		t.Fatalf("transfer to a queue = %d %s", tq.Code, tq.Raw)
	}
	if n := e.count(`SELECT count(*) FROM conversation_control_events WHERE conversation_id=$1 AND reason='crm_transfer_queue'`, conv2); n != 1 {
		t.Errorf("history rows = %d", n)
	}
}

// Finalize is the attendance's own finalization, attributed to the installation; the hold goes with it.
func TestWriteFinalizeClosesLikeTheWorkspaceAndIsAttributedToTheInstallation(t *testing.T) {
	e := newEnv(t)
	tn, _, app, conv, _ := e.writeEnv()
	tok := app.Credential.Token
	ticket := uuid.New()
	e.exec(`INSERT INTO tickets(id,tenant_id,conversation_id,subject,status) VALUES($1,$2,$3,'local','open')`, ticket, tn, conv)
	v := e.cmd(tok, conv, "claim", "key-claim-0001", "", nil).Doc.Control.Version
	if r := e.cmd(tok, conv, "finalize", "key-fin-000001", "", nil); r.Code != 428 {
		t.Errorf("finalize without If-Match = %d", r.Code)
	}
	if r := e.cmd(tok, conv, "finalize", "key-fin-000001", match(v), map[string]any{"reason": "maybe"}); r.Code != 422 {
		t.Errorf("invalid reason = %d", r.Code)
	}
	fin := e.cmd(tok, conv, "finalize", "key-fin-000001", match(v), map[string]any{"reason": "resolved", "note": "Resolvido pelo CRM"})
	if fin.Code != 200 || fin.Doc.Status != "closed" || fin.Doc.Control.State != "unassigned" || fin.Doc.ClosureID == nil {
		t.Fatalf("finalize = %d %s", fin.Code, fin.Raw)
	}
	var source string
	var byUser, byInst *uuid.UUID
	var status string
	if err := e.seed.QueryRow(e.ctx, `SELECT source, closed_by_user_id, closed_by_installation_id FROM conversation_closures WHERE conversation_id=$1`, conv).Scan(&source, &byUser, &byInst); err != nil {
		t.Fatal(err)
	}
	_ = e.seed.QueryRow(e.ctx, `SELECT status FROM tickets WHERE id=$1`, ticket).Scan(&status)
	if source != "external_app" || byUser != nil || byInst == nil || *byInst != app.Installation.ID || status != "closed" {
		t.Errorf("closure: source=%s user=%v installation=%v ticket=%s", source, byUser, byInst, status)
	}
	var cs string
	_ = e.seed.QueryRow(e.ctx, `SELECT status FROM conversations WHERE id=$1`, conv).Scan(&cs)
	if cs != "closed" {
		t.Errorf("conversation status = %s", cs)
	}
	// a retry yields the stored answer; a new key on a closed conversation is a conflict, not a second closure
	if r := e.cmd(tok, conv, "finalize", "key-fin-000001", match(v), map[string]any{"reason": "resolved", "note": "Resolvido pelo CRM"}); r.Code != 200 || r.Header.Get("Idempotent-Replay") != "true" || *r.Doc.ClosureID != *fin.Doc.ClosureID {
		t.Errorf("retry of finalize = %d %s", r.Code, r.Raw)
	}
	if r := e.cmd(tok, conv, "finalize", "key-fin-000002", match(fin.Doc.Control.Version), nil); r.Code != 409 || r.Doc.Code != "conversation_closed" {
		t.Errorf("second finalize = %d %s", r.Code, r.Raw)
	}
	if n := e.count(`SELECT count(*) FROM conversation_closures WHERE conversation_id=$1`, conv); n != 1 {
		t.Errorf("closures = %d", n)
	}
}

// Anything the installation may not see is the uniform 404 for every command, and nothing is recorded; a missing scope is 403.
func TestWriteInvisibleConversationsAndMissingScopes(t *testing.T) {
	e := newEnv(t)
	tn, admin, app, _, _ := e.writeEnv()
	tok := app.Credential.Token
	staff := e.member(tn, "tenant_agent")
	staffConv, _ := e.conv(tn, convOpts{staff: &staff})
	internalConv, _ := e.conv(tn, convOpts{contactKind: "internal"})
	spamConv, _ := e.conv(tn, convOpts{contactKind: "spam"})
	otherTenant := e.tenant()
	foreign, _ := e.conv(otherTenant, convOpts{})
	q1, q2 := e.queue(tn), e.queue(tn)
	code, body := e.admin(admin, tn, http.MethodPost, "", map[string]any{"name": "só q1", "scopes": writeScopes, "queue_ids": []string{q1.String()}})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	var limited created
	_ = json.Unmarshal(body, &limited)
	inQ2, _ := e.conv(tn, convOpts{queue: &q2})
	inQ1, _ := e.conv(tn, convOpts{queue: &q1})
	for name, c := range map[string]uuid.UUID{"staff": staffConv, "internal contact": internalConv, "spam": spamConv, "other company": foreign, "does not exist": uuid.New()} {
		for _, op := range []string{"claim", "release", "transfer", "messages", "finalize"} {
			var b any
			switch op {
			case "transfer":
				b = map[string]any{"queue_id": q1}
			case "messages":
				b = map[string]any{"text": "x"}
			}
			r := e.cmd(tok, c, op, "key-inv-"+op+"-"+strings.ReplaceAll(name, " ", "-"), match(1), b)
			if r.Code != 404 || r.Doc.Code != "not_found" {
				t.Errorf("%s on %s = %d %s, want the uniform 404", op, name, r.Code, r.Raw)
			}
		}
	}
	if r := e.cmd(limited.Credential.Token, inQ2, "claim", "key-q2-0000001", "", nil); r.Code != 404 {
		t.Errorf("claim outside the queue allow-list = %d, want 404", r.Code)
	}
	if r := e.cmd(limited.Credential.Token, inQ1, "claim", "key-q1-0000001", "", nil); r.Code != 200 {
		t.Errorf("claim inside the allow-list = %d", r.Code)
	}
	if n := e.count(`SELECT count(*) FROM external_command_receipts WHERE conversation_id = ANY($1)`, []uuid.UUID{staffConv, internalConv, spamConv, foreign, inQ2}); n != 0 {
		t.Errorf("receipts for invisible conversations: %d", n)
	}
	// a queue the installation cannot use is not a transfer target either
	vq1 := e.cmd(limited.Credential.Token, inQ1, "claim", "key-q1-0000002", "", nil).Doc.Control.Version
	if r := e.cmd(limited.Credential.Token, inQ1, "transfer", "key-trf-q2-0001", match(vq1), map[string]any{"queue_id": q2}); r.Code != 422 {
		t.Errorf("transfer to a queue outside the allow-list = %d %s", r.Code, r.Raw)
	}
	// scopes
	readOnly := e.install(admin, tn, "só leitura", "conversation.read")
	claimOnly := e.install(admin, tn, "só claim", "conversation.read", "conversation.claim")
	c2, _ := e.conv(tn, convOpts{})
	for _, c := range []struct {
		tok, op string
		want    int
	}{{readOnly.Credential.Token, "claim", 403}, {readOnly.Credential.Token, "messages", 403}, {claimOnly.Credential.Token, "claim", 200}, {claimOnly.Credential.Token, "messages", 403},
		{claimOnly.Credential.Token, "transfer", 403}, {claimOnly.Credential.Token, "finalize", 403}} {
		var b any
		switch c.op {
		case "transfer":
			b = map[string]any{"queue_id": q1}
		case "messages":
			b = map[string]any{"text": "x"}
		}
		ifm := match(1)
		if c.op == "claim" {
			ifm = "" // a claim of a free conversation needs none
		}
		if r := e.cmd(c.tok, c2, c.op, "key-scope-"+c.op+c.tok[len(c.tok)-4:], ifm, b); r.Code != c.want {
			t.Errorf("%s with a token that cannot = %d %s, want %d", c.op, r.Code, r.Raw, c.want)
		}
	}
}

// Revocation that lands after the authentication but before the write: the transaction re-checks and the command is refused; nothing is written.
func TestWriteRevocationAfterAuthenticationIsRefusedInsideTheTransaction(t *testing.T) {
	e := newEnv(t)
	tn, admin, app, conv, _ := e.writeEnv()
	tok := app.Credential.Token
	// "a revocation is in progress": the installation row is locked by another transaction
	rev, err := e.seed.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rev.Rollback(context.Background()) })
	if _, err := rev.Exec(e.ctx, `SELECT 1 FROM external_app_installations WHERE id=$1 FOR UPDATE`, app.Installation.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan cmdResp, 1)
	go func() { done <- e.cmd(tok, conv, "claim", "key-claim-0001", "", nil) }()
	time.Sleep(300 * time.Millisecond) // authenticated, now waiting in the re-check
	select {
	case r := <-done:
		t.Fatalf("the command did not wait for the revocation: %d", r.Code)
	default:
	}
	if _, err := rev.Exec(e.ctx, `UPDATE external_app_installations SET status='revoked', revoked_at=now() WHERE id=$1`, app.Installation.ID); err != nil {
		t.Fatal(err)
	}
	if err := rev.Commit(e.ctx); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.Code != 401 {
		t.Fatalf("a command after the revocation = %d %s, want 401", r.Code, r.Raw)
	}
	var kind *string
	_ = e.seed.QueryRow(e.ctx, `SELECT control_kind FROM conversations WHERE id=$1`, conv).Scan(&kind)
	if kind != nil || e.count(`SELECT count(*) FROM external_command_receipts WHERE tenant_id=$1`, tn) != 0 {
		t.Errorf("a refused command left traces: kind=%v", kind)
	}
	_ = admin
}

// Ten identical requests at once (a client that retries aggressively): one effect, one message, ten identical answers.
func TestWriteConcurrentIdenticalRequestsHaveOneEffect(t *testing.T) {
	e := newEnv(t)
	_, _, app, conv, _ := e.writeEnv()
	tok := app.Credential.Token
	v := e.cmd(tok, conv, "claim", "key-claim-0001", "", nil).Doc.Control.Version
	var wg sync.WaitGroup
	start := make(chan struct{})
	ids := make([]uuid.UUID, 10)
	codes := make([]int, 10)
	var mu sync.Mutex
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r := e.cmdSafe(tok, conv, "messages", "key-same-000001", match(v), map[string]any{"text": "uma vez só"}, &mu)
			codes[i] = r.Code
			if r.Doc.MessageID != nil {
				ids[i] = *r.Doc.MessageID
			}
		}()
	}
	close(start)
	wg.Wait()
	for i := range codes {
		if codes[i] != 202 || ids[i] != ids[0] || ids[i] == uuid.Nil {
			t.Fatalf("request %d = %d id=%s (first id %s)", i, codes[i], ids[i], ids[0])
		}
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, conv); n != 1 {
		t.Fatalf("outbound messages = %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM outbox_events WHERE aggregate_id=$1`, ids[0].String()); n != 1 {
		t.Fatalf("delivery jobs = %d, want 1", n)
	}
}

// cmdSafe is cmd for goroutines (the counter used for remote addresses is shared).
func (e *env) cmdSafe(token string, conv uuid.UUID, op, key, ifMatch string, body any, mu *sync.Mutex) cmdResp {
	mu.Lock()
	e.seq++
	n := e.seq
	mu.Unlock()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/external/conversations/"+conv.String()+"/"+op, bytes.NewReader(raw))
	req.RemoteAddr = "198.51.100." + strconv.Itoa(n%250+1) + ":" + strconv.Itoa(5000+n/250)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("If-Match", ifMatch)
	rec := httptest.NewRecorder()
	e.ext.ServeHTTP(rec, req)
	r := cmdResp{Code: rec.Code, Header: rec.Header(), Raw: rec.Body.Bytes()}
	_ = json.Unmarshal(r.Raw, &r.Doc)
	return r
}

// The race matrix. In each round the CRM and another actor go for the same conversation at the same instant; the invariants hold every time.
func TestWriteRacesWithWorkspaceRoutingTransferAndClose(t *testing.T) {
	e := newEnv(t)
	tn, _, app, _, conn := e.writeEnv()
	tok := app.Credential.Token
	q := e.queue(tn)
	e.exec(`UPDATE queues SET mode='round_robin' WHERE id=$1`, q)
	agent := e.agentInQueue(tn, q)
	rr := routingadapters.NewPostgresAssignmentRepository(e.app, nil)
	var mu sync.Mutex
	const rounds = 25

	// 1. the CRM claims while the router (round robin) and a person's claim go for the same free conversation: exactly one holder
	for i := 0; i < rounds; i++ {
		c, _ := e.conv(tn, convOpts{queue: &q, conn: &conn})
		var wg sync.WaitGroup
		var crm cmdResp
		var routed, claimed bool
		wg.Add(3)
		start := make(chan struct{})
		go func() {
			defer wg.Done()
			<-start
			crm = e.cmdSafe(tok, c, "claim", fmt.Sprintf("key-race1-%06d", i), "", nil, &mu)
		}()
		go func() {
			defer wg.Done()
			<-start
			err := platformdb.WithSystemTenantSession(e.ctx, e.app, tn, func(ctx context.Context) error {
				_, ok, err := rr.AssignRoundRobin(ctx, c, "round_robin")
				routed = ok
				return err
			})
			_ = err
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := e.seed.Exec(e.ctx, `UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1 AND assigned_to_user_id IS NULL AND control_kind IS NULL`, c, agent)
			claimed = err == nil
		}()
		close(start)
		wg.Wait()
		var kind *string
		var assigned *uuid.UUID
		var holder *uuid.UUID
		_ = e.seed.QueryRow(e.ctx, `SELECT control_kind, assigned_to_user_id, control_holder_id FROM conversations WHERE id=$1`, c).Scan(&kind, &assigned, &holder)
		crmHolds := kind != nil && *kind == "crm_application"
		if crmHolds && (assigned != nil || crm.Code != 200) {
			t.Fatalf("round %d: the CRM holds it but assigned=%v claim=%d", i, assigned, crm.Code)
		}
		if !crmHolds && crm.Code == 200 {
			t.Fatalf("round %d: the CRM was told it holds a conversation it does not (kind=%v)", i, kind)
		}
		if crmHolds && (routed || claimed) && assigned != nil {
			t.Fatalf("round %d: two holders", i)
		}
		if !crmHolds && crm.Code != 409 && crm.Code != 200 {
			t.Fatalf("round %d: the loser got %d %s", i, crm.Code, crm.Raw)
		}
	}

	// 2. a transfer and a send at the same instant, both with the version read: if the transfer won, no message of the application is newer than it
	for i := 0; i < rounds; i++ {
		c, _ := e.conv(tn, convOpts{queue: &q, conn: &conn})
		e.message(tn, c, "inbound", "oi", "received", time.Minute, nil)
		v := e.cmdSafe(tok, c, "claim", fmt.Sprintf("key-race2c-%05d", i), "", nil, &mu).Doc.Control.Version
		var send, trf cmdResp
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			send = e.cmdSafe(tok, c, "messages", fmt.Sprintf("key-race2m-%05d", i), match(v), map[string]any{"text": "oi"}, &mu)
		}()
		go func() {
			defer wg.Done()
			<-start
			trf = e.cmdSafe(tok, c, "transfer", fmt.Sprintf("key-race2t-%05d", i), match(v), map[string]any{"queue_id": q}, &mu)
		}()
		close(start)
		wg.Wait()
		if send.Code != 202 && send.Code != 409 && send.Code != 412 || trf.Code != 200 && trf.Code != 409 && trf.Code != 412 {
			t.Fatalf("round %d: send=%d %s transfer=%d %s", i, send.Code, send.Raw, trf.Code, trf.Raw)
		}
		// Both may succeed only in the order send -> transfer (a send does not change the version): after the transfer the version moved, so a send with the old
		// one is refused by the version predicate of the very statement that inserts the message. A refused send leaves no message (below).
		if send.Code == 202 && trf.Code == 200 {
			if n := e.count(`SELECT count(*) FROM messages WHERE id=$1 AND sent_by_installation_id=$2`, *send.Doc.MessageID, app.Installation.ID); n != 1 {
				t.Fatalf("round %d: the accepted message does not exist", i)
			}
		}
		if trf.Code == 200 && send.Code != 202 {
			if n := e.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, c); n != 0 {
				t.Fatalf("round %d: the send was refused but %d messages exist", i, n)
			}
		}
	}

	// 3. a finalize and a send at the same instant: nothing is queued after the close
	for i := 0; i < rounds; i++ {
		c, _ := e.conv(tn, convOpts{queue: &q, conn: &conn})
		e.message(tn, c, "inbound", "oi", "received", time.Minute, nil)
		v := e.cmdSafe(tok, c, "claim", fmt.Sprintf("key-race3c-%05d", i), "", nil, &mu).Doc.Control.Version
		var send, fin cmdResp
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			send = e.cmdSafe(tok, c, "messages", fmt.Sprintf("key-race3m-%05d", i), match(v), map[string]any{"text": "oi"}, &mu)
		}()
		go func() {
			defer wg.Done()
			<-start
			fin = e.cmdSafe(tok, c, "finalize", fmt.Sprintf("key-race3f-%05d", i), match(v), map[string]any{"reason": "resolved"}, &mu)
		}()
		close(start)
		wg.Wait()
		if fin.Code != 200 {
			t.Fatalf("round %d: finalize = %d %s", i, fin.Code, fin.Raw)
		}
		if send.Code != 202 && send.Code != 409 && send.Code != 412 {
			t.Fatalf("round %d: send = %d %s", i, send.Code, send.Raw)
		}
		if send.Code != 202 {
			if n := e.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, c); n != 0 {
				t.Fatalf("round %d: the send was refused but %d messages exist", i, n)
			}
		}
		// whatever the order, nothing can be queued after the close: the conversation is closed and the application no longer holds it
		if r := e.cmdSafe(tok, c, "messages", fmt.Sprintf("key-race3n-%05d", i), match(v+1), map[string]any{"text": "depois"}, &mu); r.Code != 409 && r.Code != 412 {
			t.Fatalf("round %d: a send after the close = %d %s", i, r.Code, r.Raw)
		}
	}

	// 4. a supervisor's takeover and a send: the message is queued before the takeover or not at all
	sup := e.member(tn, "tenant_supervisor")
	var took atomic.Int32
	for i := 0; i < rounds; i++ {
		c, _ := e.conv(tn, convOpts{queue: &q, conn: &conn})
		e.message(tn, c, "inbound", "oi", "received", time.Minute, nil)
		v := e.cmdSafe(tok, c, "claim", fmt.Sprintf("key-race4c-%05d", i), "", nil, &mu).Doc.Control.Version
		var send cmdResp
		var terr error
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			send = e.cmdSafe(tok, c, "messages", fmt.Sprintf("key-race4m-%05d", i), match(v), map[string]any{"text": "oi"}, &mu)
		}()
		go func() {
			defer wg.Done()
			<-start
			terr = platformdb.WithSystemTenantSession(e.ctx, e.app, tn, func(ctx context.Context) error {
				_, err := conversationcontrol.Service{}.TakeOver(ctx, platformdb.QuerierFromContext(ctx, e.app), tn, c, sup, "race", "req-"+strconv.Itoa(i))
				return err
			})
		}()
		close(start)
		wg.Wait()
		if terr != nil {
			t.Fatalf("round %d: takeover: %v", i, terr)
		}
		took.Add(1)
		if send.Code != 202 && send.Code != 409 && send.Code != 412 {
			t.Fatalf("round %d: send = %d %s", i, send.Code, send.Raw)
		}
		if send.Code != 202 {
			if n := e.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, c); n != 0 {
				t.Fatalf("round %d: the send was refused but %d messages exist", i, n)
			}
		}
		// whatever happened, the application can no longer write
		if r := e.cmdSafe(tok, c, "messages", fmt.Sprintf("key-race4n-%05d", i), match(v+1), map[string]any{"text": "depois"}, &mu); r.Code != 409 && r.Code != 412 {
			t.Fatalf("round %d: the application wrote after the takeover: %d %s", i, r.Code, r.Raw)
		}
	}
	if took.Load() != rounds {
		t.Fatalf("takeovers = %d", took.Load())
	}
}

// Revoking the installation while it is writing: every command that commits did so before the revocation, the hold is drained, and nothing is queued after.
func TestWriteRevocationWhileWritingDrainsTheHoldAndStopsSends(t *testing.T) {
	e := newEnv(t)
	tn, admin, app, _, conn := e.writeEnv()
	tok := app.Credential.Token
	var convs []uuid.UUID
	var vers []int64
	for i := 0; i < 8; i++ {
		c, _ := e.conv(tn, convOpts{conn: &conn})
		e.message(tn, c, "inbound", "oi", "received", time.Minute, nil)
		convs = append(convs, c)
		vers = append(vers, e.cmd(tok, c, "claim", fmt.Sprintf("key-claim-%04d", i), "", nil).Doc.Control.Version)
	}
	var mu sync.Mutex
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var late atomic.Int32
	var revokedAt atomic.Int64
	for w := 0; w < 4; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				c := convs[(w+i)%len(convs)]
				r := e.cmdSafe(tok, c, "messages", fmt.Sprintf("key-w%d-%06d", w, i), match(vers[(w+i)%len(convs)]), map[string]any{"text": "msg"}, &mu)
				if r.Code == 202 && revokedAt.Load() != 0 {
					late.Add(1) // answered 202 after the revocation had been committed and reported: must not happen
				}
			}
		}()
	}
	time.Sleep(300 * time.Millisecond)
	code, body := e.admin(admin, tn, http.MethodPost, "/"+app.Installation.ID.String()+"/revoke", nil)
	revokedAt.Store(time.Now().UnixNano())
	if code != http.StatusOK {
		close(stop)
		wg.Wait()
		t.Fatalf("revoke = %d %s", code, body)
	}
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
	if late.Load() != 0 {
		t.Fatalf("%d sends were accepted after the revocation returned", late.Load())
	}
	if n := e.count(`SELECT count(*) FROM conversations WHERE tenant_id=$1 AND control_kind='crm_application'`, tn); n != 0 {
		t.Fatalf("%d conversations are still held by a revoked installation", n)
	}
	// after the revocation nothing more can be queued: the count does not move, and every further command is refused as unauthenticated
	before := e.count(`SELECT count(*) FROM messages WHERE tenant_id=$1 AND sent_by_installation_id=$2`, tn, app.Installation.ID)
	e.addr = "203.0.113.9:4000" // the writers' failed attempts used up the throttle of their addresses; this one is fresh
	for i := 0; i < 5; i++ {
		if r := e.cmd(tok, convs[i], "messages", fmt.Sprintf("key-after-%04d", i), match(vers[i]), map[string]any{"text": "tarde"}); r.Code != 401 {
			t.Errorf("a command after the revocation = %d, want 401", r.Code)
		}
	}
	if after := e.count(`SELECT count(*) FROM messages WHERE tenant_id=$1 AND sent_by_installation_id=$2`, tn, app.Installation.ID); after != before {
		t.Fatalf("messages grew after the revocation: %d -> %d", before, after)
	}
}

// The wrong kind of caller: a person's cookie is not an integration token, an unknown token is 401, the tenant cannot be chosen.
func TestWriteAuthenticationAndTenantChoice(t *testing.T) {
	e := newEnv(t)
	tn, _, app, conv, _ := e.writeEnv()
	if r := e.cmd("", conv, "claim", "key-claim-0001", "", nil); r.Code != 401 {
		t.Errorf("no token = %d", r.Code)
	}
	if r := e.cmd("omn_ext_bogus.bogus", conv, "claim", "key-claim-0001", "", nil); r.Code != 401 {
		t.Errorf("bad token = %d", r.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/external/conversations/"+conv.String()+"/claim?tenant_id="+tn.String(), nil)
	req.RemoteAddr = "198.51.100.77:4000"
	req.Header.Set("Authorization", "Bearer "+app.Credential.Token)
	req.Header.Set("Idempotency-Key", "key-claim-0001")
	rec := httptest.NewRecorder()
	e.ext.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("tenant_id in the query = %d, want 400", rec.Code)
	}
}

// The same Idempotency-Key used at the same instant for two DIFFERENT conversations: one command wins, the other is refused, and only one effect exists.
func TestWriteSameKeyForTwoConversationsAtOnceHasOneWinner(t *testing.T) {
	e := newEnv(t)
	tn, _, app, _, conn := e.writeEnv()
	tok := app.Credential.Token
	var mu sync.Mutex
	for i := 0; i < 15; i++ {
		a, _ := e.conv(tn, convOpts{conn: &conn})
		b, _ := e.conv(tn, convOpts{conn: &conn})
		key := fmt.Sprintf("key-twin-%06d", i)
		var ra, rb cmdResp
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; ra = e.cmdSafe(tok, a, "claim", key, "", nil, &mu) }()
		go func() { defer wg.Done(); <-start; rb = e.cmdSafe(tok, b, "claim", key, "", nil, &mu) }()
		close(start)
		wg.Wait()
		ok := 0
		for _, r := range []cmdResp{ra, rb} {
			switch r.Code {
			case 200:
				ok++
			case 422:
				if r.Doc.Code != "idempotency_key_reused" {
					t.Fatalf("round %d: the loser got %s", i, r.Raw)
				}
			default:
				t.Fatalf("round %d: %d %s", i, r.Code, r.Raw)
			}
		}
		held := e.count(`SELECT count(*) FROM conversations WHERE id = ANY($1) AND control_kind='crm_application'`, []uuid.UUID{a, b})
		if ok != 1 || held != 1 {
			t.Fatalf("round %d: %d accepted, %d held; want exactly one of each", i, ok, held)
		}
	}
}

// Retries of a release / finalize / transfer that arrive together: the loser of the lock finds the receipt and gets the same answer (not "not holder" / "closed").
func TestWriteConcurrentIdenticalStateChangesAreReplays(t *testing.T) {
	e := newEnv(t)
	tn, _, app, _, conn := e.writeEnv()
	tok := app.Credential.Token
	q := e.queue(tn)
	var mu sync.Mutex
	for _, op := range []string{"release", "finalize", "transfer"} {
		c, _ := e.conv(tn, convOpts{conn: &conn})
		v := e.cmd(tok, c, "claim", "key-claim-"+op+"-1", "", nil).Doc.Control.Version
		var body any
		if op == "transfer" {
			body = map[string]any{"queue_id": q}
		}
		codes := make([]int, 8)
		versions := make([]int64, 8)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				raw, _ := json.Marshal(body)
				if body == nil {
					raw = nil
				}
				r := e.cmdSafeRaw(tok, c, op, "key-same-"+op+"-1", match(v), raw, &mu)
				codes[i], versions[i] = r.Code, r.Doc.Control.Version
			}()
		}
		close(start)
		wg.Wait()
		for i := range codes {
			if codes[i] != 200 || versions[i] != versions[0] || versions[i] != v+1 {
				t.Fatalf("%s: request %d = %d v%d (first v%d, expected v%d)", op, i, codes[i], versions[i], versions[0], v+1)
			}
		}
		if n := e.count(`SELECT count(*) FROM external_command_receipts WHERE conversation_id=$1 AND operation=$2`, c, op); n != 1 {
			t.Fatalf("%s: receipts = %d", op, n)
		}
	}
}

func (e *env) cmdSafeRaw(token string, conv uuid.UUID, op, key, ifMatch string, raw []byte, mu *sync.Mutex) cmdResp {
	mu.Lock()
	e.seq++
	n := e.seq
	mu.Unlock()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/external/conversations/"+conv.String()+"/"+op, bytes.NewReader(raw))
	req.RemoteAddr = "198.51.100." + strconv.Itoa(n%250+1) + ":" + strconv.Itoa(6000+n/250)
	req.Header.Set("Authorization", "Bearer "+token)
	if len(raw) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("If-Match", ifMatch)
	rec := httptest.NewRecorder()
	e.ext.ServeHTTP(rec, req)
	r := cmdResp{Code: rec.Code, Header: rec.Header(), Raw: rec.Body.Bytes()}
	_ = json.Unmarshal(r.Raw, &r.Doc)
	return r
}

// Outside the sector's calendar an installation may not take a conversation, unless the administrator allowed it for that installation.
func TestWriteClaimOutsideTheQueuesHoursNeedsThePermission(t *testing.T) {
	e := newEnv(t)
	tn, admin, app, _, conn := e.writeEnv()
	closedCal := uuid.New()
	e.exec(`INSERT INTO calendars(id,tenant_id,name,timezone,kind,weekly) VALUES($1,$2,'fechado','America/Manaus','custom','[{"day":"mon","intervals":[]}]'::jsonb)`, closedCal, tn)
	q := e.queue(tn)
	e.exec(`UPDATE queues SET calendar_id=$2 WHERE id=$1`, q, closedCal)
	conv, _ := e.conv(tn, convOpts{queue: &q, conn: &conn})
	r := e.cmd(app.Credential.Token, conv, "claim", "key-hours-00001", "", nil)
	if r.Code != 409 || r.Doc.Code != "outside_hours" {
		t.Fatalf("claim while the queue is closed = %d %s", r.Code, r.Raw)
	}
	if n := e.count(`SELECT count(*) FROM conversations WHERE id=$1 AND control_kind IS NOT NULL`, conv); n != 0 {
		t.Fatalf("a refused claim took the conversation")
	}
	code, body := e.admin(admin, tn, http.MethodPost, "", map[string]any{"name": "CRM 24h", "scopes": writeScopes, "allow_outside_hours": true})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	var allowed created
	_ = json.Unmarshal(body, &allowed)
	if r := e.cmd(allowed.Credential.Token, conv, "claim", "key-hours-00002", "", nil); r.Code != 200 {
		t.Fatalf("claim by an installation allowed outside hours = %d %s", r.Code, r.Raw)
	}
	// it keeps what it holds: repeating the claim after the hours closed again is not refused
	if r := e.cmd(allowed.Credential.Token, conv, "claim", "key-hours-00003", "", nil); r.Code != 200 || r.Doc.Changed == nil || *r.Doc.Changed {
		t.Errorf("repeated claim = %d %s", r.Code, r.Raw)
	}
}

// A receipt is evidence: the application role can add and read, never change or delete; old ones leave only through the purge function.
func TestWriteReceiptsAreAppendOnlyAndPurgedOnlyByTheFunction(t *testing.T) {
	e := newEnv(t)
	tn, _, app, conv, _ := e.writeEnv()
	if r := e.cmd(app.Credential.Token, conv, "claim", "key-claim-0001", "", nil); r.Code != 200 {
		t.Fatalf("claim = %d", r.Code)
	}
	for name, stmt := range map[string]string{"update": `UPDATE external_command_receipts SET status_code = 200`, "delete": `DELETE FROM external_command_receipts`} {
		err := platformdb.WithSystemTenantSession(e.ctx, e.app, tn, func(ctx context.Context) error {
			_, err := platformdb.QuerierFromContext(ctx, e.app).Exec(ctx, stmt)
			return err
		})
		var pe *pgconn.PgError
		if !asPg(err, &pe) || pe.Code != "42501" {
			t.Errorf("%s of a receipt by the application role: %v, want permission denied", name, err)
		}
	}
	var gone int64
	if err := e.app.QueryRow(e.ctx, `SELECT external_command_receipts_purge(interval '1 day')`).Scan(&gone); err != nil || gone != 0 {
		t.Fatalf("purge of fresh receipts removed %d (%v)", gone, err)
	}
	e.exec(`UPDATE external_command_receipts SET created_at = now() - interval '3 days' WHERE tenant_id=$1`, tn)
	if err := e.app.QueryRow(e.ctx, `SELECT external_command_receipts_purge(interval '2 days')`).Scan(&gone); err != nil || gone != 1 {
		t.Fatalf("purge of old receipts removed %d (%v)", gone, err)
	}
	if _, err := e.app.Exec(e.ctx, `SELECT external_command_receipts_purge(interval '1 hour')`); err == nil {
		t.Error("a retention shorter than a day must be refused")
	}
}

// The key is scoped to the installation: two applications of one company may use the same key without ever seeing each other's receipts.
func TestWriteIdempotencyKeysAreScopedToTheInstallation(t *testing.T) {
	e := newEnv(t)
	tn, admin, a, convA, conn := e.writeEnv()
	b := e.install(admin, tn, "CRM B", writeScopes...)
	convB, _ := e.conv(tn, convOpts{conn: &conn})
	const key = "key-shared-0001"
	ra := e.cmd(a.Credential.Token, convA, "claim", key, "", nil)
	rb := e.cmd(b.Credential.Token, convB, "claim", key, "", nil)
	if ra.Code != 200 || rb.Code != 200 || ra.Header.Get("Idempotent-Replay") != "" || rb.Header.Get("Idempotent-Replay") != "" {
		t.Fatalf("A=%d B=%d: the same key from two installations is two commands", ra.Code, rb.Code)
	}
	// B repeating A's command with the same key is B's own command: A holds convA, so B is refused (never handed A's receipt)
	if r := e.cmd(b.Credential.Token, convA, "claim", key, "", nil); r.Code != 422 && r.Code != 409 {
		t.Errorf("B with A's key on A's conversation = %d %s", r.Code, r.Raw)
	} else if r.Header.Get("Idempotent-Replay") != "" || (r.Doc.Control.Holder != nil && r.Doc.Control.Holder.Mine) {
		t.Errorf("B was answered with A's receipt: %s", r.Raw)
	}
	if n := e.count(`SELECT count(DISTINCT installation_id) FROM external_command_receipts WHERE tenant_id=$1 AND idempotency_key=$2`, tn, key); n != 2 {
		t.Errorf("receipts for the key belong to %d installations, want 2", n)
	}
}
