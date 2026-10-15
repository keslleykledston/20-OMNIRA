package adapters_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/external/adapters"
)

// Regressions for the final transversal Codex review: what an application queued must not leave after somebody else took the conversation, and a hold must never be
// left behind by an application that can no longer act.

func (e *env) heldWithQueuedMessage(tn uuid.UUID, tok string, q, conn uuid.UUID) (conv, msg uuid.UUID) {
	e.t.Helper()
	conv, _ = e.conv(tn, convOpts{queue: &q, conn: &conn})
	e.message(tn, conv, "inbound", "oi", "received", 0, nil)
	v := e.cmd(tok, conv, "claim", "key-fin-"+uuid.NewString()[:8], "", nil).Doc.Control.Version
	r := e.cmd(tok, conv, "messages", "key-fin-"+uuid.NewString()[:8], match(v), map[string]any{"text": "ainda na fila"})
	if r.Code != 202 || r.Doc.MessageID == nil {
		e.t.Fatalf("queue a message = %d %s", r.Code, r.Raw)
	}
	return conv, *r.Doc.MessageID
}

func (e *env) msgState(id uuid.UUID) (status, reason string) {
	_ = e.seed.QueryRow(e.ctx, `SELECT status, failure_reason FROM messages WHERE id=$1`, id).Scan(&status, &reason)
	return
}

// A message queued while the application held the conversation does not go out after control was taken from it AGAINST its will; when the application itself lets go,
// what it queued before is its own and goes out.
func TestQueuedMessagesOfAnApplicationDieWhenControlIsTakenAwayNotWhenItLetsGo(t *testing.T) {
	e := newEnv(t)
	e.workspaceAPI()
	tn, admin, app, _, conn := e.writeEnv()
	tok := app.Credential.Token
	sup := e.member(tn, "tenant_supervisor")
	q := e.queue(tn)
	e.exec(`UPDATE queues SET mode='round_robin' WHERE id=$1`, q)
	e.exec(`UPDATE external_app_installations SET lease_seconds=300 WHERE id=$1`, app.Installation.ID)

	// 1. a supervisor's takeover
	c1, m1 := e.heldWithQueuedMessage(tn, tok, q, conn)
	if code, body := e.ws(sup, tn, "/inbox/conversations/"+c1.String()+"/takeover", map[string]any{"reason": "cliente pediu humano", "confirm": true}); code != 200 {
		t.Fatalf("takeover = %d %s", code, body)
	}
	if st, why := e.msgState(m1); st != "failed" || why != "control_lost" {
		t.Errorf("takeover: the queued message is %s/%s, want failed/control_lost", st, why)
	}
	// 2. an expired lease
	c2, m2 := e.heldWithQueuedMessage(tn, tok, q, conn)
	e.exec(`UPDATE external_conversation_leases SET lease_until = now() - interval '10 minutes' WHERE conversation_id=$1`, c2)
	if n, err := adapters.SweepLeases(e.ctx, e.app, adapters.LeaseGrace, 50, tn); err != nil || n < 1 {
		t.Fatalf("sweep = %d %v", n, err)
	}
	if st, why := e.msgState(m2); st != "failed" || why != "control_lost" {
		t.Errorf("lease expiry: %s/%s", st, why)
	}
	// 3. the CRM itself releases, transfers or finalizes after queueing: its message still goes out
	for _, op := range []string{"release", "transfer", "finalize"} {
		c, m := e.heldWithQueuedMessage(tn, tok, q, conn)
		var v int64
		_ = e.seed.QueryRow(e.ctx, `SELECT control_version FROM conversations WHERE id=$1`, c).Scan(&v)
		var body any
		if op == "transfer" {
			body = map[string]any{"queue_id": q}
		}
		if r := e.cmd(tok, c, op, "key-fin-"+op+"-"+uuid.NewString()[:6], match(v), body); r.Code != 200 {
			t.Fatalf("%s = %d %s", op, r.Code, r.Raw)
		}
		if st, _ := e.msgState(m); st != "queued" {
			t.Errorf("%s by the application itself cancelled its own queued message (%s)", op, st)
		}
	}
	// 4. suspension and revocation by the administrator
	for _, op := range []string{"suspend", "revoke"} {
		a := e.install(admin, tn, "CRM "+op, writeScopes...)
		e.exec(`UPDATE external_app_installations SET rate_limit_per_min=6000 WHERE id=$1`, a.Installation.ID)
		c, m := e.heldWithQueuedMessage(tn, a.Credential.Token, q, conn)
		if code, body := e.admin(admin, tn, http.MethodPost, "/"+a.Installation.ID.String()+"/"+op, nil); code != 200 {
			t.Fatalf("%s = %d %s", op, code, body)
		}
		if st, why := e.msgState(m); st != "failed" || why != "control_lost" {
			t.Errorf("%s: %s/%s", op, st, why)
		}
		var kind *string
		_ = e.seed.QueryRow(e.ctx, `SELECT control_kind FROM conversations WHERE id=$1`, c).Scan(&kind)
		if kind != nil {
			t.Errorf("%s left the conversation held", op)
		}
	}
}

// An application whose permission was narrowed, or that has no usable token, cannot keep a conversation it can no longer touch.
func TestHoldsAreFreedWhenTheApplicationCanNoLongerAct(t *testing.T) {
	e := newEnv(t)
	tn, admin, _, _, conn := e.writeEnv()
	q1, q2 := e.queue(tn), e.queue(tn)
	held := func(a created, queue uuid.UUID) uuid.UUID {
		c, _ := e.conv(tn, convOpts{queue: &queue, conn: &conn})
		if r := e.cmd(a.Credential.Token, c, "claim", "key-narrow-"+uuid.NewString()[:8], "", nil); r.Code != 200 {
			t.Fatalf("claim = %d %s", r.Code, r.Raw)
		}
		return c
	}
	freed := func(c uuid.UUID) bool {
		var kind *string
		_ = e.seed.QueryRow(e.ctx, `SELECT control_kind FROM conversations WHERE id=$1`, c).Scan(&kind)
		return kind == nil
	}
	newApp := func(name string) created {
		a := e.install(admin, tn, name, writeScopes...)
		e.exec(`UPDATE external_app_installations SET rate_limit_per_min=6000 WHERE id=$1`, a.Installation.ID)
		return a
	}
	// the queue leaves the allow-list; broadening it frees nothing
	a := newApp("estreita fila")
	c1, c2 := held(a, q1), held(a, q2)
	if code, _ := e.admin(admin, tn, http.MethodPatch, "/"+a.Installation.ID.String(), map[string]any{"queue_ids": []string{q1.String(), q2.String()}}); code != 200 || freed(c1) || freed(c2) {
		t.Fatalf("widening the allow-list: code=%d freed=%v/%v", code, freed(c1), freed(c2))
	}
	if code, _ := e.admin(admin, tn, http.MethodPatch, "/"+a.Installation.ID.String(), map[string]any{"queue_ids": []string{q1.String()}}); code != 200 {
		t.Fatal("narrow")
	}
	if freed(c1) || !freed(c2) {
		t.Errorf("after narrowing to q1: c1 freed=%v (want false) c2 freed=%v (want true)", freed(c1), freed(c2))
	}
	// the line leaves the allow-list
	b := newApp("estreita linha")
	cb := held(b, q1)
	other := e.connection(tn)
	if code, _ := e.admin(admin, tn, http.MethodPatch, "/"+b.Installation.ID.String(), map[string]any{"channel_ids": []string{other.String()}}); code != 200 || !freed(cb) {
		t.Errorf("line allow-list: code=%d freed=%v", code, freed(cb))
	}
	// the permission to hold is removed
	c := newApp("sem claim")
	cc := held(c, q1)
	if code, _ := e.admin(admin, tn, http.MethodPatch, "/"+c.Installation.ID.String(), map[string]any{"scopes": []string{"conversation.read", "message.read"}}); code != 200 || !freed(cc) {
		t.Errorf("scope removed: code=%d freed=%v", code, freed(cc))
	}
	// a change that touches neither (a new name) frees nothing
	d := newApp("so renomeia")
	cd := held(d, q1)
	if code, _ := e.admin(admin, tn, http.MethodPatch, "/"+d.Installation.ID.String(), map[string]any{"name": "renomeada"}); code != 200 || freed(cd) {
		t.Errorf("rename: code=%d freed=%v", code, freed(cd))
	}
	// the last usable token is revoked: freed at once; with another token left nothing happens
	f := newApp("um token so")
	cf := held(f, q1)
	var credID uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT id FROM external_app_credentials WHERE installation_id=$1`, f.Installation.ID).Scan(&credID)
	second := e.admin2(admin, tn, http.MethodPost, "/"+f.Installation.ID.String()+"/credentials", map[string]any{"label": "segundo"})
	if second != 201 {
		t.Fatalf("second credential = %d", second)
	}
	if code, _ := e.admin(admin, tn, http.MethodDelete, "/"+f.Installation.ID.String()+"/credentials/"+credID.String(), nil); code != 204 || freed(cf) {
		t.Errorf("revoking one of two tokens: code=%d freed=%v (want 204, held)", code, freed(cf))
	}
	var credID2 uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT id FROM external_app_credentials WHERE installation_id=$1 AND revoked_at IS NULL`, f.Installation.ID).Scan(&credID2)
	if code, _ := e.admin(admin, tn, http.MethodDelete, "/"+f.Installation.ID.String()+"/credentials/"+credID2.String(), nil); code != 204 || !freed(cf) {
		t.Errorf("revoking the last token: code=%d freed=%v", code, freed(cf))
	}
	// the tokens simply expire: the worker gives the conversation back
	g := newApp("tokens expiram")
	cg := held(g, q1)
	e.exec(`UPDATE external_app_credentials SET created_at = now() - interval '2 days', expires_at = now() - interval '1 minute' WHERE installation_id=$1`, g.Installation.ID)
	if freed(cg) {
		t.Fatal("setup: freed too early")
	}
	if n, err := adapters.SweepLeases(e.ctx, e.app, adapters.LeaseGrace, 50, tn); err != nil || n < 1 || !freed(cg) {
		t.Errorf("expired tokens: sweep freed %d (%v), conversation freed=%v", n, err, freed(cg))
	}
	if e.count(`SELECT count(*) FROM conversation_control_events WHERE conversation_id=$1 AND reason='no_credential'`, cg) != 1 {
		t.Error("the reason no_credential is not in the history")
	}
}

func (e *env) admin2(user, tenant uuid.UUID, method, path string, body any) int {
	code, _ := e.admin(user, tenant, method, path, body)
	return code
}
