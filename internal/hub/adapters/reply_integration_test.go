package adapters_test

// Write path of the Hub (claim + reply) on a real PostgreSQL, through the real handler, the real UserSessionMiddleware
// and the real send store. The point of these tests is the isolation of WRITES: a Hub agent can only claim and answer
// where a live, reply-capable grant says so, only as themselves, only for the item's real tenant, and the database
// itself still refuses every direct write by that agent (no INSERT/UPDATE policy was added for delegation).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/hub/adapters"
	"github.com/omnira/omnira/internal/hub/application"
	"github.com/omnira/omnira/internal/hub/replying"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// connect gives every conversation of the tenant an active text channel and its own contact (the schema allows one
// open conversation per contact and channel, and the fixture created several for the same contact).
func (w *world) connect(tenantKey string) {
	conn := uuid.New()
	w.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
	        VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]')`, conn, w.tenant[tenantKey], conn.String())
	rows, err := w.owner.Query(w.ctx, `SELECT id FROM conversations WHERE tenant_id = $1`, w.tenant[tenantKey])
	w.must(err)
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		w.must(rows.Scan(&id))
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		contact := uuid.New()
		w.exec(`INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES ($1, $2, 'Cliente', $3)`,
			contact, w.tenant[tenantKey], fmt.Sprintf("+5511%09d", rand.Intn(1_000_000_000)))
		w.exec(`UPDATE conversations SET channel_connection_id = $1, contact_id = $2 WHERE id = $3`, conn, contact, id)
	}
}

// grantReply is grant() plus the reply capability.
func (w *world) grantReply(user uuid.UUID, tenantKey string) uuid.UUID {
	id := w.grant(user, tenantKey)
	w.exec(`UPDATE effective_access_grants SET can_reply = true WHERE id = $1`, id)
	return id
}

func (a *hubAPI) post(user, hub, item uuid.UUID, action string, body any, hdr map[string]string) (int, string, http.Header) {
	a.w.t.Helper()
	raw, err := json.Marshal(body)
	a.w.must(err)
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/hubs/%s/inbox/%s/%s", a.srv.URL, hub, item, action), bytes.NewReader(raw))
	a.w.must(err)
	req.Header.Set("Content-Type", "application/json")
	if user != uuid.Nil {
		req.Header.Set("X-Test-User", user.String())
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	a.w.must(err)
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out), resp.Header
}

func (a *hubAPI) claim(user uuid.UUID, itemKey, expectedTenantKey string) int {
	a.w.t.Helper()
	code, _, _ := a.post(user, a.w.hub, a.w.itemID(itemKey), "claim", map[string]any{"expected_tenant_id": a.w.tenantFor(expectedTenantKey)}, nil)
	return code
}

func (a *hubAPI) reply(user uuid.UUID, itemKey, expectedTenantKey, text, key string) (int, string, http.Header) {
	a.w.t.Helper()
	return a.post(user, a.w.hub, a.w.itemID(itemKey), "messages",
		map[string]any{"expected_tenant_id": a.w.tenantFor(expectedTenantKey), "text": text}, map[string]string{"Idempotency-Key": key})
}

// tenantFor returns the tenant id for "A","B","C" or the nil uuid for "" (nothing displayed).
func (w *world) tenantFor(key string) uuid.UUID {
	if key == "" {
		return uuid.Nil
	}
	return w.tenant[key]
}

func (w *world) assignee(convKey string) *uuid.UUID {
	var u *uuid.UUID
	w.must(w.owner.QueryRow(w.ctx, `SELECT assigned_to_user_id FROM conversations WHERE id = $1`, w.conv[convKey]).Scan(&u))
	return u
}

func (w *world) outbound(convKey string) int {
	var n int
	w.must(w.owner.QueryRow(w.ctx, `SELECT count(*) FROM messages WHERE conversation_id = $1 AND direction = 'outbound'`, w.conv[convKey]).Scan(&n))
	return n
}

func (w *world) count(sql string, args ...any) int {
	var n int
	w.must(w.owner.QueryRow(w.ctx, sql, args...).Scan(&n))
	return n
}

func TestHubReply_CapabilityAndHappyPath(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	w.connect("A")
	w.connect("B")
	reader := w.hubAgent("reader")
	w.grant(reader, "A") // read-only
	alice := w.hubAgent("alice")
	w.grantReply(alice, "A")
	w.grant(alice, "B") // reads B but cannot reply there

	t.Run("a read-only grant can read but neither claim nor reply: 403, nothing written", func(t *testing.T) {
		if code, _ := api.list(reader, ""); code != 200 {
			t.Fatalf("read-only agents still read: %d", code)
		}
		if code := api.claim(reader, "A", "A"); code != 403 {
			t.Fatalf("claim: %d, want 403", code)
		}
		if code, body, _ := api.reply(reader, "A", "A", "oi", "key-reader-0001"); code != 403 {
			t.Fatalf("reply: %d %s, want 403", code, body)
		}
		if w.assignee("A") != nil || w.outbound("A") != 0 {
			t.Fatal("a read-only grant changed the conversation")
		}
	})
	t.Run("replying before claiming is a 409, not an implicit claim", func(t *testing.T) {
		if code, body, _ := api.reply(alice, "A", "A", "oi", "key-alice-0001"); code != 409 {
			t.Fatalf("%d %s", code, body)
		}
		if w.assignee("A") != nil || w.outbound("A") != 0 {
			t.Fatal("reply claimed or wrote without a claim")
		}
	})
	t.Run("claim then reply: assigned to the agent, queued, audited, answers as the item's company", func(t *testing.T) {
		if code := api.claim(alice, "A", "A"); code != 200 {
			t.Fatalf("claim %d", code)
		}
		if a := w.assignee("A"); a == nil || *a != alice {
			t.Fatalf("assignee = %v", a)
		}
		if n := w.count(`SELECT count(*) FROM assignment_events WHERE conversation_id = $1 AND reason = 'hub_claim' AND changed_by = $2`, w.conv["A"], alice); n != 1 {
			t.Fatalf("assignment history rows: %d", n)
		}
		code, body, _ := api.reply(alice, "A", "A", "Olá, em que posso ajudar?", "key-alice-0002")
		if code != 202 {
			t.Fatalf("reply %d %s", code, body)
		}
		var got struct {
			ID     uuid.UUID `json:"id"`
			Status string    `json:"status"`
			Tenant struct {
				ID   uuid.UUID `json:"id"`
				Name string    `json:"name"`
			} `json:"tenant"`
		}
		w.must(json.Unmarshal([]byte(body), &got))
		if got.Status != "queued" || got.Tenant.ID != w.tenant["A"] || got.Tenant.Name == "" {
			t.Fatalf("response: %+v", got)
		}
		if n := w.count(`SELECT count(*) FROM messages WHERE id = $1 AND tenant_id = $2 AND sent_by_user_id = $3 AND direction = 'outbound'`, got.ID, w.tenant["A"], alice); n != 1 {
			t.Fatal("message not stored under tenant A as alice")
		}
		if n := w.count(`SELECT count(*) FROM outbox_events WHERE tenant_id = $1 AND aggregate_id = $2 AND event_type = 'job.channel.send_text.v1'`, w.tenant["A"], got.ID.String()); n != 1 {
			t.Fatalf("delivery jobs: %d", n)
		}
		for _, action := range []string{"hub.conversation.claimed", "hub.message.sent"} {
			if n := w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND actor_id = $2 AND action = $3`, w.tenant["A"], alice, action); n != 1 {
				t.Errorf("audit %s: %d rows", action, n)
			}
		}
	})
	t.Run("idempotency: same key and text replays (200), same key and other text is 422, one message only", func(t *testing.T) {
		code, _, h := api.reply(alice, "A", "A", "Olá, em que posso ajudar?", "key-alice-0002")
		if code != 200 || h.Get("Idempotent-Replayed") != "true" {
			t.Fatalf("replay: %d %v", code, h)
		}
		if code, _, _ := api.reply(alice, "A", "A", "outro texto", "key-alice-0002"); code != 422 {
			t.Fatalf("mismatch: %d", code)
		}
		if n := w.outbound("A"); n != 1 {
			t.Fatalf("outbound messages: %d", n)
		}
	})
	t.Run("claiming again is an idempotent no-op", func(t *testing.T) {
		if code := api.claim(alice, "A", "A"); code != 200 {
			t.Fatalf("%d", code)
		}
		if n := w.count(`SELECT count(*) FROM assignment_events WHERE conversation_id = $1`, w.conv["A"]); n != 1 {
			t.Fatalf("history rows: %d", n)
		}
	})
	t.Run("readable but not reply-capable tenant B: 403 even though alice can read it", func(t *testing.T) {
		if code := api.claim(alice, "B", "B"); code != 403 {
			t.Fatalf("%d", code)
		}
		if code, _, _ := api.reply(alice, "B", "B", "oi", "key-alice-0003"); code != 403 {
			t.Fatalf("%d", code)
		}
		if w.assignee("B") != nil || w.outbound("B") != 0 {
			t.Fatal("B was written")
		}
	})
}

func TestHubReply_IsolationAndForgery(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	w.connect("A")
	w.connect("B")
	w.connect("C")
	alice := w.hubAgent("alice")
	w.grantReply(alice, "A")
	carol := w.user("carol") // direct member of A, not in the hub
	w.directMember(carol, "A")
	outsider := w.user("outsider")
	noGrant := w.hubAgent("nogrant")

	untouched := func(t *testing.T, keys ...string) {
		t.Helper()
		for _, k := range keys {
			if w.assignee(k) != nil || w.outbound(k) != 0 {
				t.Errorf("conversation %s was touched", k)
			}
		}
	}
	t.Run("tenant without any grant (C) is 404 for claim and reply, nothing written", func(t *testing.T) {
		if code := api.claim(alice, "C", "C"); code != 404 {
			t.Fatalf("claim C: %d", code)
		}
		if code, _, _ := api.reply(alice, "C", "C", "oi", "key-alice-0004"); code != 404 {
			t.Fatalf("reply C: %d", code)
		}
		untouched(t, "C")
	})
	t.Run("a hub member with no grants, a direct tenant member and an outsider all get 404", func(t *testing.T) {
		for name, u := range map[string]uuid.UUID{"nogrant": noGrant, "carol": carol, "outsider": outsider} {
			if code := api.claim(u, "A", "A"); code != 404 {
				t.Errorf("%s claim: %d", name, code)
			}
			if code, _, _ := api.reply(u, "A", "A", "oi", "key-"+name+"-0001"); code != 404 {
				t.Errorf("%s reply: %d", name, code)
			}
		}
		untouched(t, "A")
	})
	t.Run("forged hub id and unknown item are 404; malformed ids are 400", func(t *testing.T) {
		body := map[string]any{"expected_tenant_id": w.tenant["A"]}
		if code, _, _ := api.post(alice, uuid.New(), w.itemID("A"), "claim", body, nil); code != 404 {
			t.Errorf("forged hub: %d", code)
		}
		if code, _, _ := api.post(alice, w.hub, uuid.New(), "claim", body, nil); code != 404 {
			t.Errorf("unknown item: %d", code)
		}
		if code, _, _ := api.do("POST", "/api/v1/hubs/not-a-uuid/inbox/"+uuid.NewString()+"/claim", alice, nil); code != 400 {
			t.Errorf("malformed hub: %d", code)
		}
		untouched(t, "A")
	})
	t.Run("the company on screen must be the item's company: mismatch or missing is 409 and writes nothing", func(t *testing.T) {
		for name, shown := range map[string]string{"shown B while item is A": "B", "shown C": "C", "nothing shown": ""} {
			if code := api.claim(alice, "A", shown); code != 409 {
				t.Errorf("%s: claim %d", name, code)
			}
			if code, _, _ := api.reply(alice, "A", shown, "oi", "key-alice-0005"); code != 409 {
				t.Errorf("%s: reply %d", name, code)
			}
		}
		untouched(t, "A")
	})
	t.Run("tenant selectors, unknown body fields and bad payloads are rejected", func(t *testing.T) {
		item := w.itemID("A")
		path := fmt.Sprintf("%s/api/v1/hubs/%s/inbox/%s/claim?tenant_id=%s", api.srv.URL, w.hub, item, w.tenant["C"])
		req, _ := http.NewRequest("POST", path, bytes.NewReader([]byte(`{}`)))
		req.Header.Set("X-Test-User", alice.String())
		resp, err := http.DefaultClient.Do(req)
		w.must(err)
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("?tenant_id on a write: %d", resp.StatusCode)
		}
		if code, _, _ := api.post(alice, w.hub, item, "claim", map[string]any{"expected_tenant_id": w.tenant["A"], "tenant_id": w.tenant["C"]}, nil); code != 400 {
			t.Errorf("unknown field tenant_id in body: %d", code)
		}
		untouched(t, "A", "C")
	})
	t.Run("the database itself still refuses direct writes by a hub agent (no write policy was added)", func(t *testing.T) {
		if n, _ := w.write(alice, `UPDATE conversations SET assigned_to_user_id = $1 WHERE tenant_id = $2`, alice, w.tenant["A"]); n != 0 {
			t.Errorf("UPDATE conversations affected %d rows", n)
		}
		if _, err := w.write(alice, `INSERT INTO messages (tenant_id, conversation_id, direction, message_type, body, status, sent_by_user_id) VALUES ($1,$2,'outbound','text','x','queued',$3)`,
			w.tenant["A"], w.conv["A"], alice); err == nil {
			t.Error("INSERT INTO messages by a hub agent succeeded")
		}
		if _, err := w.write(alice, `INSERT INTO outbox_events (tenant_id, event_type, aggregate_type, aggregate_id, payload) VALUES ($1,'job.channel.send_text.v1','message','x','{}')`, w.tenant["A"]); err == nil {
			t.Error("INSERT INTO outbox_events by a hub agent succeeded")
		}
		untouched(t, "A")
	})
}

func TestHubReply_ExclusiveAssignmentAndRace(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	w.connect("A")
	alice, bob := w.hubAgent("alice"), w.hubAgent("bob")
	w.grantReply(alice, "A")
	w.grantReply(bob, "A")

	if code := api.claim(alice, "A", "A"); code != 200 {
		t.Fatalf("alice claim %d", code)
	}
	if code := api.claim(bob, "A", "A"); code != 409 {
		t.Fatalf("bob claims alice's conversation: %d, want 409", code)
	}
	if code, _, _ := api.reply(bob, "A", "A", "oi", "key-bob-0001"); code != 409 {
		t.Fatalf("bob replies to alice's conversation: %d, want 409", code)
	}
	if a := w.assignee("A"); a == nil || *a != alice || w.outbound("A") != 0 {
		t.Fatal("bob changed alice's conversation")
	}

	t.Run("two agents claiming an unassigned conversation at once: exactly one wins", func(t *testing.T) {
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i, u := range []uuid.UUID{alice, bob} {
			wg.Add(1)
			go func() { defer wg.Done(); codes[i] = api.claim(u, "A2", "A") }()
		}
		wg.Wait()
		wins := 0
		for _, c := range codes {
			if c == 200 {
				wins++
			} else if c != 409 {
				t.Errorf("unexpected status %d", c)
			}
		}
		if wins != 1 {
			t.Fatalf("winners: %d (codes %v)", wins, codes)
		}
	})
}

func TestHubReply_RevocationAndScopeAreHonoured(t *testing.T) {
	setup := func(t *testing.T) (*world, *hubAPI, uuid.UUID, uuid.UUID) {
		w := newWorld(t)
		api := newHubAPI(t, w)
		w.connect("A")
		alice := w.hubAgent("alice")
		grant := w.grantReply(alice, "A")
		if code := api.claim(alice, "A", "A"); code != 200 {
			t.Fatalf("claim %d", code)
		}
		if code, body, _ := api.reply(alice, "A", "A", "primeira", "key-setup-0001"); code != 202 {
			t.Fatalf("first reply %d %s", code, body)
		}
		return w, api, alice, grant
	}
	cases := []struct {
		name   string
		mutate func(w *world, alice, grant uuid.UUID)
		want   int
	}{
		{"grant revoked", func(w *world, _, g uuid.UUID) {
			w.exec(`UPDATE effective_access_grants SET status='revoked' WHERE id=$1`, g)
		}, 404},
		{"grant expired", func(w *world, _, g uuid.UUID) {
			w.exec(`UPDATE effective_access_grants SET valid_from = now() - interval '2 days', valid_until = now() - interval '1 day' WHERE id=$1`, g)
		}, 404},
		{"contract revoked", func(w *world, _, _ uuid.UUID) {
			w.exec(`UPDATE hub_tenant_service_contracts SET status='revoked' WHERE id=$1`, w.contract["A"])
		}, 404},
		{"hub suspended", func(w *world, _, _ uuid.UUID) {
			w.exec(`UPDATE service_hubs SET status='suspended' WHERE id=$1`, w.hub)
		}, 404},
		{"agent removed from the hub", func(w *world, a, _ uuid.UUID) { w.exec(`DELETE FROM hub_memberships WHERE user_id=$1`, a) }, 404},
		{"reply capability removed (still readable)", func(w *world, _, g uuid.UUID) {
			w.exec(`UPDATE effective_access_grants SET can_reply=false WHERE id=$1`, g)
		}, 403},
		{"contract narrowed to another queue", func(w *world, _, _ uuid.UUID) {
			w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = jsonb_build_object('queue_ids', jsonb_build_array($2::text)) WHERE id=$1`, w.contract["A"], w.queue2["A"].String())
		}, 404},
		{"conversation moved to a queue outside the contract scope", func(w *world, _, _ uuid.UUID) {
			w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = jsonb_build_object('queue_ids', jsonb_build_array($2::text)) WHERE id=$1`, w.contract["A"], w.queue1["A"].String())
			w.exec(`UPDATE conversations SET queue_id=$2 WHERE id=$1`, w.conv["A"], w.queue2["A"])
		}, 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, api, alice, grant := setup(t)
			c.mutate(w, alice, grant)
			before := w.outbound("A")
			if code, body, _ := api.reply(alice, "A", "A", "depois", "key-after-0001"); code != c.want {
				t.Errorf("reply: %d %s, want %d", code, body, c.want)
			}
			if code := api.claim(alice, "A", "A"); code != c.want {
				t.Errorf("claim: %d, want %d", code, c.want)
			}
			if w.outbound("A") != before {
				t.Error("a message was queued after the delegation ended")
			}
		})
	}
	t.Run("finalized conversation: 409 for claim-less reply and for a new claim", func(t *testing.T) {
		w, api, alice, _ := setup(t)
		w.exec(`UPDATE conversations SET status='closed' WHERE id=$1`, w.conv["A"])
		if code, _, _ := api.reply(alice, "A", "A", "tarde demais", "key-closed-0001"); code != 409 {
			t.Errorf("reply on closed: %d", code)
		}
		if code := api.claim(alice, "A", "A"); code != 409 {
			t.Errorf("claim on closed: %d", code)
		}
		if w.outbound("A") != 1 {
			t.Error("message queued into a closed conversation")
		}
	})
}

// A grant revoked AFTER the request was authorized but BEFORE the write must stop the write: the service re-checks
// the delegation inside the writing transaction (the application-level check alone would let it through).
func TestHubReply_RevokedBetweenAuthorizeAndWrite(t *testing.T) {
	w := newWorld(t)
	w.connect("A")
	alice := w.hubAgent("alice")
	grant := w.grantReply(alice, "A")
	repo := adapters.NewPostgresHubRepository(w.app)
	svc := replying.New(w.app, application.NewHubAuthorizationService(repo), repo, messagesadapters.NewPostgresOutboundStore(w.app))
	item := w.itemID("A")

	var target *replying.Target
	w.must(platformdb.WithTenantSession(w.ctx, w.app, alice, false, func(c context.Context) error {
		var err error
		target, err = svc.Authorize(c, alice, w.hub, item, w.tenant["A"], "corr-1")
		return err
	}))
	w.exec(`UPDATE effective_access_grants SET status='revoked' WHERE id=$1`, grant) // revoked after authorization

	if _, err := svc.Claim(w.ctx, alice, target); err == nil || err.Error() != application.ErrAccessDenied.Error() {
		t.Fatalf("claim after revocation: %v, want access denied", err)
	}
	if w.assignee("A") != nil {
		t.Fatal("claimed with a revoked grant")
	}
	// even with the conversation assigned to alice by someone legitimate, a revoked grant cannot send
	w.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, w.conv["A"], alice)
	if _, err := svc.Send(w.ctx, alice, target, "oi", "key-race-00001"); err == nil || err.Error() != application.ErrAccessDenied.Error() {
		t.Fatalf("send after revocation: %v, want access denied", err)
	}
	if w.outbound("A") != 0 {
		t.Fatal("message queued with a revoked grant")
	}
}

// The reply capability itself is re-checked in the writing transaction, not only when the request is authorized.
func TestHubReply_CapabilityRemovedBetweenAuthorizeAndWrite(t *testing.T) {
	w := newWorld(t)
	w.connect("A")
	alice := w.hubAgent("alice")
	grant := w.grantReply(alice, "A")
	repo := adapters.NewPostgresHubRepository(w.app)
	svc := replying.New(w.app, application.NewHubAuthorizationService(repo), repo, messagesadapters.NewPostgresOutboundStore(w.app))

	var target *replying.Target
	w.must(platformdb.WithTenantSession(w.ctx, w.app, alice, false, func(c context.Context) error {
		var err error
		target, err = svc.Authorize(c, alice, w.hub, w.itemID("A"), w.tenant["A"], "corr-2")
		return err
	}))
	w.exec(`UPDATE effective_access_grants SET can_reply = false WHERE id = $1`, grant)
	w.exec(`UPDATE conversations SET assigned_to_user_id = $2 WHERE id = $1`, w.conv["A"], alice)
	if _, err := svc.Send(w.ctx, alice, target, "oi", "key-cap-000001"); err == nil || err.Error() != application.ErrAccessDenied.Error() {
		t.Fatalf("send after the capability was removed: %v, want access denied", err)
	}
	if w.outbound("A") != 0 {
		t.Fatal("message queued without the reply capability")
	}
}
