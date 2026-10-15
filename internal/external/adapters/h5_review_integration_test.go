package adapters_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/external/adapters"
)

// Regressions for the Codex review of H5: permission judged again under the lock, replay after the view changed, capacity under concurrency, the CRM lease,
// the unverified agent label, the 403 for a queue outside the allow-list, one JSON object per body.

func (e *env) limitedInstall(admin, tenant uuid.UUID, queues ...uuid.UUID) created {
	e.t.Helper()
	ids := make([]string, len(queues))
	for i, q := range queues {
		ids[i] = q.String()
	}
	code, body := e.admin(admin, tenant, http.MethodPost, "", map[string]any{"name": "limitada " + uuid.NewString()[:6], "scopes": writeScopes, "queue_ids": ids, "rate_limit_per_min": 6000})
	if code != http.StatusCreated {
		e.t.Fatalf("create = %d %s", code, body)
	}
	var c created
	_ = json.Unmarshal(body, &c)
	return c
}

// While the command waits for the lock of the conversation, the world changes (the queue leaves the installation's allow-list, or the contact becomes spam). The
// command must notice: 404, no receipt, no effect.
func TestWriteVisibilityIsJudgedAgainWithTheConversationLocked(t *testing.T) {
	e := newEnv(t)
	tn, admin, _, _, conn := e.writeEnv()
	q1, q2 := e.queue(tn), e.queue(tn)
	app := e.limitedInstall(admin, tn, q1)
	for name, change := range map[string]string{
		"the queue leaves the allow-list": `UPDATE conversations SET queue_id=$2 WHERE id=$1`,
		"the contact becomes spam":        `UPDATE contacts SET kind='spam' WHERE id=(SELECT contact_id FROM conversations WHERE id=$1) AND $2::uuid IS NOT NULL`,
	} {
		conv, _ := e.conv(tn, convOpts{queue: &q1, conn: &conn})
		hold, err := e.seed.Begin(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := hold.Exec(e.ctx, `SELECT 1 FROM conversations WHERE id=$1 FOR UPDATE`, conv); err != nil {
			t.Fatal(err)
		}
		done := make(chan cmdResp, 1)
		go func() {
			done <- e.cmd(app.Credential.Token, conv, "claim", "key-toctou-"+strings.ReplaceAll(name, " ", "-"), "", nil)
		}()
		time.Sleep(300 * time.Millisecond) // it saw the conversation as visible and is now waiting for the lock
		if _, err := hold.Exec(e.ctx, change, conv, q2); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := hold.Commit(e.ctx); err != nil {
			t.Fatal(err)
		}
		r := <-done
		if r.Code != 404 {
			t.Errorf("%s: the command ran on a conversation that left the installation's view: %d %s", name, r.Code, r.Raw)
		}
		var kind *string
		_ = e.seed.QueryRow(e.ctx, `SELECT control_kind FROM conversations WHERE id=$1`, conv).Scan(&kind)
		if kind != nil || e.count(`SELECT count(*) FROM external_command_receipts WHERE conversation_id=$1`, conv) != 0 {
			t.Errorf("%s: a refused command left traces (kind=%v)", name, kind)
		}
	}
}

// A command that was done keeps its answer for a retry even after the conversation left the installation's view; a new command there is the uniform 404.
func TestWriteReplayOutlivesAChangeOfVisibility(t *testing.T) {
	e := newEnv(t)
	tn, admin, _, _, conn := e.writeEnv()
	q1, q2 := e.queue(tn), e.queue(tn)
	app := e.limitedInstall(admin, tn, q1)
	tok := app.Credential.Token
	conv, contact := e.conv(tn, convOpts{queue: &q1, conn: &conn})
	e.message(tn, conv, "inbound", "oi", "received", time.Minute, nil)
	v := e.cmd(tok, conv, "claim", "key-claim-0001", "", nil).Doc.Control.Version
	sent := e.cmd(tok, conv, "messages", "key-msg-00001", match(v), map[string]any{"text": "uma vez"})
	if sent.Code != 202 {
		t.Fatalf("send = %d %s", sent.Code, sent.Raw)
	}
	e.exec(`UPDATE conversations SET queue_id=$2 WHERE id=$1`, conv, q2) // out of the allow-list
	e.exec(`UPDATE contacts SET kind='spam' WHERE id=$1`, contact)
	r := e.cmd(tok, conv, "messages", "key-msg-00001", match(v), map[string]any{"text": "uma vez"})
	if r.Code != 202 || r.Header.Get("Idempotent-Replay") != "true" || r.Doc.MessageID == nil || *r.Doc.MessageID != *sent.Doc.MessageID {
		t.Fatalf("retry after the view changed = %d %s, want the same receipt", r.Code, r.Raw)
	}
	if r := e.cmd(tok, conv, "messages", "key-msg-00002", match(v), map[string]any{"text": "outra"}); r.Code != 404 {
		t.Errorf("a NEW command on a conversation that left the view = %d, want 404", r.Code)
	}
	if r := e.cmd(tok, conv, "messages", "key-msg-00001", match(v), map[string]any{"text": "mudou"}); r.Code != 422 && r.Code != 404 {
		t.Errorf("the key with another payload = %d, want 422 (or 404)", r.Code)
	}
}

// Capacity is spent under the same locks the router uses: two applications' transfers to one person who has room for ONE cannot both succeed.
func TestWriteTransferRespectsCapacityUnderConcurrency(t *testing.T) {
	e := newEnv(t)
	tn, _, app, _, conn := e.writeEnv()
	tok := app.Credential.Token
	q := e.queue(tn)
	agent := e.agentInQueue(tn, q)
	e.exec(`UPDATE queue_members SET capacity=1 WHERE queue_id=$1 AND user_id=$2`, q, agent)
	var mu sync.Mutex
	for round := 0; round < 12; round++ {
		e.exec(`UPDATE conversations SET assigned_to_user_id=NULL WHERE tenant_id=$1 AND assigned_to_user_id=$2 AND control_kind='tenant_user'`, tn, agent) // not used; keeps rounds independent
		e.exec(`UPDATE conversations SET status='closed', closed_at=now() WHERE tenant_id=$1 AND queue_id=$2 AND status='open'`, tn, q)
		var convs [2]uuid.UUID
		var vers [2]int64
		for i := range convs {
			convs[i], _ = e.conv(tn, convOpts{queue: &q, conn: &conn})
			vers[i] = e.cmdSafe(tok, convs[i], "claim", fmt.Sprintf("key-cap-c%02d-%d", round, i), "", nil, &mu).Doc.Control.Version
		}
		var res [2]cmdResp
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range convs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				res[i] = e.cmdSafe(tok, convs[i], "transfer", fmt.Sprintf("key-cap-t%02d-%d", round, i), match(vers[i]), map[string]any{"queue_id": q, "user_id": agent}, &mu)
			}()
		}
		close(start)
		wg.Wait()
		ok, refused := 0, 0
		for _, r := range res {
			switch r.Code {
			case 200:
				ok++
			case 422:
				refused++
			default:
				t.Fatalf("round %d: %d %s", round, r.Code, r.Raw)
			}
		}
		assigned := e.count(`SELECT count(*) FROM conversations WHERE tenant_id=$1 AND queue_id=$2 AND assigned_to_user_id=$3 AND status='open'`, tn, q, agent)
		if ok != 1 || refused != 1 || assigned != 1 {
			t.Fatalf("round %d: %d accepted, %d refused, %d assigned to a person with room for one", round, ok, refused, assigned)
		}
	}
}

// The lease of a CRM hold: renewed by any command and by the heartbeat, enforced by the worker with the grace, never for an installation without a lease.
func TestWriteLeaseIsRenewedExpiredAndRaceFree(t *testing.T) {
	e := newEnv(t)
	tn, _, app, _, conn := e.writeEnv()
	tok := app.Credential.Token
	e.exec(`UPDATE external_app_installations SET lease_seconds=300 WHERE id=$1`, app.Installation.ID)
	q := e.queue(tn)
	e.exec(`UPDATE queues SET mode='round_robin' WHERE id=$1`, q)
	conv, _ := e.conv(tn, convOpts{queue: &q, conn: &conn})
	e.message(tn, conv, "inbound", "oi", "received", time.Minute, nil)
	until := func() time.Time {
		var t0 time.Time
		_ = e.seed.QueryRow(e.ctx, `SELECT lease_until FROM external_conversation_leases WHERE conversation_id=$1`, conv).Scan(&t0)
		return t0
	}
	claim := e.cmd(tok, conv, "claim", "key-lease-0001", "", nil)
	v := claim.Doc.Control.Version
	first := until()
	if d := time.Until(first); d < 250*time.Second || d > 310*time.Second {
		t.Fatalf("the lease after a claim ends in %v, want ~300s", d)
	}
	e.exec(`UPDATE external_conversation_leases SET lease_until = now() + interval '10 seconds' WHERE conversation_id=$1`, conv)
	if r := e.cmd(tok, conv, "heartbeat", "key-lease-hb01", match(v), nil); r.Code != 200 || r.Doc.Changed == nil || *r.Doc.Changed {
		t.Fatalf("heartbeat = %d %s", r.Code, r.Raw)
	}
	if d := time.Until(until()); d < 250*time.Second {
		t.Fatalf("the heartbeat did not renew the lease (ends in %v)", d)
	}
	if r := e.cmd(tok, conv, "heartbeat", "key-lease-hb02", match(v+4), nil); r.Code != 412 {
		t.Errorf("heartbeat with a stale version = %d, want 412 (the application learns it lost the conversation)", r.Code)
	}
	// still within the lease: the worker leaves it alone
	if n, err := adapters.SweepLeases(e.ctx, e.app, adapters.LeaseGrace, 50, tn); err != nil || n != 0 {
		t.Fatalf("sweep with a live lease freed %d (%v)", n, err)
	}
	// ran out, but inside the grace period: still left alone
	e.exec(`UPDATE external_conversation_leases SET lease_until = now() - interval '30 seconds' WHERE conversation_id=$1`, conv)
	if n, _ := adapters.SweepLeases(e.ctx, e.app, adapters.LeaseGrace, 50, tn); n != 0 {
		t.Fatalf("sweep inside the grace period freed %d", n)
	}
	// ran out, listed by the worker, and RENEWED before the worker judged it: the renewal wins
	e.exec(`UPDATE external_conversation_leases SET lease_until = now() - interval '10 minutes' WHERE conversation_id=$1`, conv)
	adapters.LeaseSweepHookAfterDue = func() {
		e.exec(`UPDATE external_conversation_leases SET lease_until = now() + interval '5 minutes' WHERE conversation_id=$1`, conv)
	}
	n, err := adapters.SweepLeases(e.ctx, e.app, adapters.LeaseGrace, 50, tn)
	adapters.LeaseSweepHookAfterDue = nil
	if err != nil || n != 0 {
		t.Fatalf("a lease renewed after it was listed was freed anyway: %d (%v)"+"", n, err)
	}
	var stillHeld *string
	_ = e.seed.QueryRow(e.ctx, `SELECT control_kind FROM conversations WHERE id=$1`, conv).Scan(&stillHeld)
	if stillHeld == nil {
		t.Fatal("the conversation was freed although the application had renewed")
	}
	// ran out for good: freed with its reason, the router is asked, the application finds out by the version
	e.exec(`UPDATE external_conversation_leases SET lease_until = now() - interval '10 minutes' WHERE conversation_id=$1`, conv)
	if n, err := adapters.SweepLeases(e.ctx, e.app, adapters.LeaseGrace, 50, tn); err != nil || n != 1 {
		t.Fatalf("sweep of an expired lease freed %d (%v)", n, err)
	}
	var kind *string
	var ver int64
	_ = e.seed.QueryRow(e.ctx, `SELECT control_kind, control_version FROM conversations WHERE id=$1`, conv).Scan(&kind, &ver)
	if kind != nil || ver != v+1 {
		t.Fatalf("after the expiry: kind=%v version=%d (want free, v%d)", kind, ver, v+1)
	}
	if e.count(`SELECT count(*) FROM conversation_control_events WHERE conversation_id=$1 AND reason='lease_expired' AND actor_kind='system'`, conv) != 1 {
		t.Error("the expiry is not in the control history")
	}
	if e.count(`SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND aggregate_id=$2 AND event_type='job.routing.assign.v1'`, tn, conv.String()) < 1 {
		t.Error("the router was not asked to look at the conversation again")
	}
	if e.count(`SELECT count(*) FROM external_conversation_leases WHERE conversation_id=$1`, conv) != 0 {
		t.Error("the lease row of a freed conversation remains")
	}
	if r := e.cmd(tok, conv, "messages", "key-lease-msg1", match(v), map[string]any{"text": "tarde"}); r.Code != 412 && r.Code != 409 {
		t.Errorf("the application writing after the expiry = %d %s", r.Code, r.Raw)
	}
	// an installation with no lease never gets one
	e.exec(`UPDATE external_app_installations SET lease_seconds=NULL WHERE id=$1`, app.Installation.ID)
	conv2, _ := e.conv(tn, convOpts{queue: &q, conn: &conn})
	e.cmd(tok, conv2, "claim", "key-lease-0002", "", nil)
	if e.count(`SELECT count(*) FROM external_conversation_leases WHERE conversation_id=$1`, conv2) != 0 {
		t.Error("an installation without a lease got one")
	}
	// the sweeper and the heartbeat at the same instant: if the heartbeat is accepted the conversation is still held; if it was freed the heartbeat is refused
	e.exec(`UPDATE external_app_installations SET lease_seconds=300 WHERE id=$1`, app.Installation.ID)
	var mu sync.Mutex
	var heldAndOK, freed atomic.Int32
	for i := 0; i < 20; i++ {
		c, _ := e.conv(tn, convOpts{queue: &q, conn: &conn})
		cv := e.cmdSafe(tok, c, "claim", fmt.Sprintf("key-lease-r%03d", i), "", nil, &mu).Doc.Control.Version
		e.exec(`UPDATE external_conversation_leases SET lease_until = now() - interval '10 minutes' WHERE conversation_id=$1`, c)
		var hb cmdResp
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			hb = e.cmdSafe(tok, c, "heartbeat", fmt.Sprintf("key-lease-h%03d", i), match(cv), nil, &mu)
		}()
		go func() { defer wg.Done(); <-start; _, _ = adapters.SweepLeases(e.ctx, e.app, adapters.LeaseGrace, 500, tn) }()
		close(start)
		wg.Wait()
		var k *string
		_ = e.seed.QueryRow(e.ctx, `SELECT control_kind FROM conversations WHERE id=$1`, c).Scan(&k)
		switch {
		case hb.Code == 200 && k != nil && *k == "crm_application":
			heldAndOK.Add(1)
		case hb.Code != 200 && k == nil:
			freed.Add(1)
		default:
			t.Fatalf("round %d: heartbeat=%d %s but the conversation is held=%v", i, hb.Code, hb.Raw, k)
		}
	}
	if heldAndOK.Load()+freed.Load() != 20 {
		t.Fatal("inconsistent rounds")
	}
}

// What the application says about its own operator is a label, kept as unverified metadata, never an identity.
func TestWriteExternalAgentLabelIsUnverifiedMetadata(t *testing.T) {
	e := newEnv(t)
	tn, _, app, conv, _ := e.writeEnv()
	tok := app.Credential.Token
	if r := e.cmd(tok, conv, "claim", "key-agent-0001", "", map[string]any{"external_agent_id": "<script>"}); r.Code != 422 {
		t.Errorf("an unsafe label = %d, want 422", r.Code)
	}
	if r := e.cmd(tok, conv, "claim", "key-agent-0002", "", map[string]any{"external_agent_id": strings.Repeat("a", 81)}); r.Code != 422 {
		t.Errorf("a label over 80 characters = %d, want 422", r.Code)
	}
	r := e.cmd(tok, conv, "claim", "key-agent-0003", "", map[string]any{"external_agent_id": "operador@crm-7"})
	if r.Code != 200 {
		t.Fatalf("claim with a label = %d %s", r.Code, r.Raw)
	}
	var ref *string
	var actor *uuid.UUID
	var kind string
	if err := e.seed.QueryRow(e.ctx, `SELECT claimed_agent_ref, actor_id, actor_kind FROM conversation_control_events WHERE conversation_id=$1 AND reason='crm_claim'`, conv).Scan(&ref, &actor, &kind); err != nil {
		t.Fatal(err)
	}
	if ref == nil || *ref != "operador@crm-7" || kind != "crm_application" || actor == nil || *actor != app.Installation.ID {
		t.Errorf("history: ref=%v actor=%v kind=%s", ref, actor, kind)
	}
	var meta string
	_ = e.seed.QueryRow(e.ctx, `SELECT metadata::text FROM audit_events WHERE tenant_id=$1 AND metadata->>'operation'='claim' ORDER BY created_at DESC LIMIT 1`, tn).Scan(&meta)
	if !strings.Contains(meta, `"external_agent_verified": false`) || !strings.Contains(meta, "operador@crm-7") {
		t.Errorf("audit metadata = %s", meta)
	}
	// the label is part of the request: the same key with another label is another request
	if r := e.cmd(tok, conv, "claim", "key-agent-0003", "", map[string]any{"external_agent_id": "outro"}); r.Code != 422 {
		t.Errorf("same key, another label = %d, want 422", r.Code)
	}
}

// One JSON object per body: a second document after the first is not silently ignored.
func TestWriteBodyIsExactlyOneJSONObject(t *testing.T) {
	e := newEnv(t)
	_, _, app, conv, _ := e.writeEnv()
	tok := app.Credential.Token
	v := e.cmd(tok, conv, "claim", "key-body-00001", "", nil).Doc.Control.Version
	for name, body := range map[string]string{"two documents": `{"text":"a"}{"text":"b"}`, "two with a newline": "{\"text\":\"a\"}\n{\"text\":\"b\"}", "trailing garbage": `{"text":"a"} x`, "an array": `[{"text":"a"}]`} {
		r := e.cmd(tok, conv, "messages", "key-body-"+strings.ReplaceAll(name, " ", "-"), match(v), body)
		if r.Code != 422 {
			t.Errorf("%s = %d %s, want 422", name, r.Code, r.Raw)
		}
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, conv); n != 0 {
		t.Errorf("a malformed body queued %d messages", n)
	}
	if r := e.cmd(tok, conv, "messages", "key-body-ok-0001", match(v), `{"text":"a"} `); r.Code != 202 {
		t.Errorf("trailing whitespace is fine: %d %s", r.Code, r.Raw)
	}
}

var _ = context.Background
