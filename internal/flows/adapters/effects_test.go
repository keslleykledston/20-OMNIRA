package adapters_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/adapters"
	"github.com/omnira/omnira/internal/flows/flowstest"
	"github.com/omnira/omnira/internal/flows/ports"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

type fx struct {
	t       *testing.T
	env     *flowstest.Env
	effects *adapters.PostgresEffects
}

func newFx(t *testing.T) *fx {
	env := flowstest.New(t)
	sender := messagesapplication.NewSystemSender(messagesadapters.NewPostgresOutboundStore(env.App))
	return &fx{t: t, env: env, effects: adapters.NewPostgresEffects(env.App, sender)}
}

func (f *fx) sys(tenant uuid.UUID, fn func(ctx context.Context)) {
	f.t.Helper()
	f.env.AsSystem(f.t, tenant, fn)
}

func (f *fx) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.env.Seed.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("%v\n%s", err, sql)
	}
}

func (f *fx) count(sql string, args ...any) (n int) {
	f.t.Helper()
	if err := f.env.Seed.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return
}

func (f *fx) queue(tenant uuid.UUID, name, mode string, isDefault bool) uuid.UUID {
	id := uuid.New()
	f.exec(`INSERT INTO queues(id, tenant_id, name, mode, is_default) VALUES($1,$2,$3,$4,$5)`, id, tenant, name, mode, isDefault)
	return id
}

func (f *fx) account(tenant uuid.UUID, name string) uuid.UUID {
	id := uuid.New()
	f.exec(`INSERT INTO customer_accounts(id, tenant_id, name) VALUES($1,$2,$3)`, id, tenant, name)
	return id
}

func (f *fx) link(tenant, contact, account uuid.UUID, primary bool) {
	f.exec(`INSERT INTO contact_account_links(tenant_id, contact_id, account_id, source, is_primary) VALUES($1,$2,$3,'manual',$4)`, tenant, contact, account, primary)
}

func TestCustomerCandidatesAndCompanyValidationAreBoundToTheContact(t *testing.T) {
	f := newFx(t)
	env := f.env
	convA, contactA := env.SeedConversation(t, env.TenantA, "bot")
	_, otherContact := env.SeedConversation(t, env.TenantA, "none")
	acme, beta, foreign := f.account(env.TenantA, "ACME"), f.account(env.TenantA, "Beta"), f.account(env.TenantB, "Foreign")
	f.link(env.TenantA, contactA, acme, false)
	f.link(env.TenantA, contactA, beta, true)
	f.link(env.TenantA, otherContact, f.account(env.TenantA, "NotMine"), true)

	f.sys(env.TenantA, func(ctx context.Context) {
		c, err := f.effects.CustomerCandidates(ctx, contactA)
		if err != nil || len(c) != 2 || c[0].AccountID != beta {
			t.Fatalf("candidates (primary first): %v %v", c, err)
		}
		if err := f.effects.ValidateCustomer(ctx, convA, acme); err != nil {
			t.Fatalf("a linked company can be selected: %v", err)
		}
	})
	// ADR-0017/0018: a conversation carries no company (it can have several subjects); validation writes nothing.
	if n := f.count(`SELECT count(*) FROM information_schema.columns WHERE table_name='conversations' AND column_name LIKE '%account%'`); n != 0 {
		t.Fatal("the conversation must not carry a company column")
	}
	// A company the contact is not linked to (another contact's, another tenant's) can never be set.
	for name, bad := range map[string]uuid.UUID{"another contact's company": f.accountOf(otherContact), "another tenant's company": foreign, "unknown id": uuid.New()} {
		f.sys(env.TenantA, func(ctx context.Context) {})
		err := platformdbSession(context.Background(), env, func(sc context.Context) error {
			return f.effects.ValidateCustomer(withTenant(sc, env.TenantA, uuid.Nil), convA, bad)
		})
		if err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// A link that ended is not a candidate any more.
	f.exec(`UPDATE contact_account_links SET status='ended', is_primary=false, ended_at=now() WHERE tenant_id=$1 AND contact_id=$2 AND account_id=$3`, env.TenantA, contactA, acme)
	f.sys(env.TenantA, func(ctx context.Context) {
		if c, _ := f.effects.CustomerCandidates(ctx, contactA); len(c) != 1 || c[0].AccountID != beta {
			t.Fatalf("an ended link must not be offered: %v", c)
		}
	})
	// Tenant B's session never sees tenant A's links.
	f.sys(env.TenantB, func(ctx context.Context) {
		if c, _ := f.effects.CustomerCandidates(ctx, contactA); len(c) != 0 {
			t.Fatalf("cross-tenant candidates leaked: %v", c)
		}
	})
}

func (f *fx) accountOf(contact uuid.UUID) uuid.UUID {
	var id uuid.UUID
	if err := f.env.Seed.QueryRow(context.Background(), `SELECT account_id FROM contact_account_links WHERE contact_id=$1`, contact).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func TestEnsureTicketAdoptsThePlaceholderOnceAndNeverOverwritesARealTicket(t *testing.T) {
	f := newFx(t)
	env := f.env
	conv, _ := env.SeedConversation(t, env.TenantA, "bot")
	f.exec(`INSERT INTO tickets(tenant_id, conversation_id) VALUES($1,$2)`, env.TenantA, conv) // the ingest's placeholder (empty subject)
	var first uuid.UUID
	f.sys(env.TenantA, func(ctx context.Context) {
		id, created, err := f.effects.EnsureTicket(ctx, conv, "VPN caiu", "high", nil)
		if err != nil || !created {
			t.Fatalf("adopt: %v %v", created, err)
		}
		first = id
		id2, created2, err := f.effects.EnsureTicket(ctx, conv, "Outro assunto", "low", nil)
		if err != nil || created2 || id2 != first {
			t.Fatalf("second call must be a no-op on the same ticket: %v %v %v", id2, created2, err)
		}
	})
	var subject, priority string
	_ = env.Seed.QueryRow(context.Background(), `SELECT subject, priority FROM tickets WHERE conversation_id=$1`, conv).Scan(&subject, &priority)
	if subject != "VPN caiu" || priority != "high" || f.count(`SELECT count(*) FROM tickets WHERE conversation_id=$1`, conv) != 1 {
		t.Fatalf("one real ticket expected: %q %q", subject, priority)
	}
	// An ERP-backed ticket is never touched.
	conv2, _ := env.SeedConversation(t, env.TenantA, "bot")
	f.exec(`INSERT INTO tickets(tenant_id, conversation_id, subject, provider, external_ticket_id) VALUES($1,$2,'ERP subject','k3g','ERP-1')`, env.TenantA, conv2)
	f.sys(env.TenantA, func(ctx context.Context) {
		if _, created, err := f.effects.EnsureTicket(ctx, conv2, "bot subject", "low", nil); err != nil || created {
			t.Fatalf("an ERP ticket must be left alone: %v %v", created, err)
		}
	})
	if f.count(`SELECT count(*) FROM tickets WHERE conversation_id=$1 AND subject='ERP subject'`, conv2) != 1 {
		t.Fatal("the ERP ticket was modified")
	}
	// No ticket yet: one is created (the ingest normally made the placeholder, but a flow must not depend on it).
	conv3, _ := env.SeedConversation(t, env.TenantA, "bot")
	f.sys(env.TenantA, func(ctx context.Context) {
		if _, created, err := f.effects.EnsureTicket(ctx, conv3, "novo", "medium", nil); err != nil || !created {
			t.Fatalf("create: %v %v", created, err)
		}
	})
	// Open tickets of the contact: placeholders do not count, real ones do.
	f.sys(env.TenantA, func(ctx context.Context) {
		facts := &ports.ConversationFacts{ContactID: f.contactOf(conv)}
		sum, err := f.effects.OpenTickets(ctx, facts)
		if err != nil || sum.Count != 1 || sum.FirstSubject != "VPN caiu" {
			t.Fatalf("open tickets: %+v %v", sum, err)
		}
	})
}

func (f *fx) contactOf(conv uuid.UUID) *uuid.UUID {
	var id uuid.UUID
	if err := f.env.Seed.QueryRow(context.Background(), `SELECT contact_id FROM conversations WHERE id=$1`, conv).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return &id
}

func TestAssignQueueHandoffAndRoundRobinJob(t *testing.T) {
	f := newFx(t)
	env := f.env
	manual := f.queue(env.TenantA, "manual", "manual", false)
	rr := f.queue(env.TenantA, "rr", "round_robin", false)
	def := f.queue(env.TenantA, "default", "manual", true)
	foreign := f.queue(env.TenantB, "foreign", "manual", false)
	conv, _ := env.SeedConversation(t, env.TenantA, "bot")

	f.sys(env.TenantA, func(ctx context.Context) {
		if err := f.effects.AssignQueue(ctx, conv, &manual); err != nil {
			t.Fatal(err)
		}
	})
	if f.count(`SELECT count(*) FROM conversations WHERE id=$1 AND queue_id=$2`, conv, manual) != 1 || f.count(`SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='job.routing.assign.v1'`, conv.String()) != 0 {
		t.Fatal("manual queue: assigned, no routing job")
	}
	f.sys(env.TenantA, func(ctx context.Context) {
		if err := f.effects.AssignQueue(ctx, conv, &rr); err != nil {
			t.Fatal(err)
		}
	})
	if f.count(`SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='job.routing.assign.v1'`, conv.String()) != 1 {
		t.Fatal("a round-robin queue must enqueue the same routing job the ingest enqueues")
	}
	// Handoff without an explicit queue keeps the choice already made (it must not revert to the default).
	f.sys(env.TenantA, func(ctx context.Context) {
		if err := f.effects.Handoff(ctx, conv, nil); err != nil {
			t.Fatal(err)
		}
	})
	if f.count(`SELECT count(*) FROM conversations WHERE id=$1 AND queue_id=$2 AND automation_mode='waiting_human'`, conv, rr) != 1 {
		t.Fatal("handoff must keep the queue and set waiting_human")
	}
	// A conversation with no queue goes to the default one.
	conv2, _ := env.SeedConversation(t, env.TenantA, "bot")
	f.sys(env.TenantA, func(ctx context.Context) {
		if err := f.effects.AssignQueue(ctx, conv2, nil); err != nil {
			t.Fatal(err)
		}
	})
	if f.count(`SELECT count(*) FROM conversations WHERE id=$1 AND queue_id=$2`, conv2, def) != 1 {
		t.Fatal("default queue expected")
	}
	// Another tenant's queue (or a deleted one) is an error, never a silent no-op, and changes nothing.
	conv3, _ := env.SeedConversation(t, env.TenantA, "bot")
	err := platformdbSession(context.Background(), env, func(sc context.Context) error {
		return f.effects.AssignQueue(withTenant(sc, env.TenantA, uuid.Nil), conv3, &foreign)
	})
	if err == nil || f.count(`SELECT count(*) FROM conversations WHERE id=$1 AND queue_id IS NOT NULL`, conv3) != 0 {
		t.Fatalf("a foreign queue must be refused: %v", err)
	}
}

func TestSystemSendEndToEnd(t *testing.T) {
	f := newFx(t)
	env := f.env
	conv, _ := env.SeedConversation(t, env.TenantA, "bot")
	line := uuid.New()
	f.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]')`, line, env.TenantA, line.String())
	f.exec(`UPDATE conversations SET channel_connection_id=$2 WHERE id=$1`, conv, line)
	env.SeedInbound(t, env.TenantA, conv, "oi")

	key := "flow:" + uuid.NewString() + ":1"
	for i, want := range []ports.SendStatus{ports.SendQueued, ports.SendReplayed} {
		f.sys(env.TenantA, func(ctx context.Context) {
			got, err := f.effects.SendText(ctx, conv, "Olá!", key)
			if err != nil || got != want {
				t.Fatalf("send #%d: %v %v want %v", i+1, got, err, want)
			}
		})
	}
	if f.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound' AND sent_by_user_id IS NULL`, conv) != 1 ||
		f.count(`SELECT count(*) FROM outbox_events WHERE event_type='job.channel.send_text.v1' AND aggregate_id IN (SELECT id::text FROM messages WHERE conversation_id=$1)`, conv) != 1 {
		t.Fatal("exactly one message and one delivery job (replay inserted nothing)")
	}
	var payload string
	_ = env.Seed.QueryRow(context.Background(), `SELECT payload::text FROM outbox_events WHERE event_type='job.channel.send_text.v1' AND aggregate_id IN (SELECT id::text FROM messages WHERE conversation_id=$1)`, conv).Scan(&payload)
	if strings.Contains(payload, "Olá") || strings.Contains(payload, "+55") {
		t.Fatalf("the queue carries a reference only, never text or phone: %s", payload)
	}
	// Same key with different content is refused (never silently re-sent or altered).
	err := platformdbSession(context.Background(), env, func(sc context.Context) error {
		ctx := withSystemTenant(sc, env.TenantA)
		_, err := f.effects.SendText(ctx, conv, "Outro texto", key)
		return err
	})
	if err == nil {
		t.Fatal("same idempotency key with another text must be refused")
	}
	// A human takes the conversation: the bot goes silent (no message, no error).
	op := uuid.New()
	f.exec(`INSERT INTO users(id, external_subject, email, status) VALUES($1,$2,$3,'active')`, op, op, op.String()+"@invalid")
	t.Cleanup(func() { _, _ = env.Seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, op) })
	f.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, conv, op)
	f.sys(env.TenantA, func(ctx context.Context) {
		if got, err := f.effects.SendText(ctx, conv, "Ainda aí?", "flow:"+uuid.NewString()+":2"); err != nil || got != ports.SendNoChannel {
			t.Fatalf("assigned conversation: %v %v", got, err)
		}
	})
	if f.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, conv) != 1 {
		t.Fatal("the bot talked over an operator")
	}
}

func TestSystemSendRespectsTheMetaWindow(t *testing.T) {
	f := newFx(t)
	env := f.env
	conv, _ := env.SeedConversation(t, env.TenantA, "bot")
	line := uuid.New()
	f.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','meta_cloud','official',$3,'active','["text"]')`, line, env.TenantA, line.String())
	f.exec(`UPDATE conversations SET channel_connection_id=$2 WHERE id=$1`, conv, line)
	msg := env.SeedInbound(t, env.TenantA, conv, "oi")
	f.exec(`UPDATE messages SET created_at=$2 WHERE id=$1`, msg, time.Now().Add(-30*time.Hour))
	f.sys(env.TenantA, func(ctx context.Context) {
		if got, err := f.effects.SendText(ctx, conv, "Olá", "flow:"+uuid.NewString()+":1"); err != nil || got != ports.SendWindowClosed {
			t.Fatalf("closed 24h window: %v %v", got, err)
		}
	})
	if f.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, conv) != 0 {
		t.Fatal("nothing may be sent outside the window")
	}
	_ = platformdb.QuerierFromContext
}

// A ticket opened in the CRM/ERP from the inbox is real (it has the ERP number) but can have an empty local subject: the flow
// must name it by its number, never as an empty pair of quotes.
func TestOpenTicketsNamesAnERPTicketWithoutSubjectByItsNumber(t *testing.T) {
	f := newFx(t)
	env := f.env
	conv, _ := env.SeedConversation(t, env.TenantA, "bot")
	f.exec(`INSERT INTO tickets(tenant_id, conversation_id, provider, external_ticket_id) VALUES($1,$2,'k3g','28276')`, env.TenantA, conv)
	f.sys(env.TenantA, func(ctx context.Context) {
		sum, err := f.effects.OpenTickets(ctx, &ports.ConversationFacts{ContactID: f.contactOf(conv)})
		if err != nil || sum.Count != 1 || sum.FirstSubject != "nº 28276" {
			t.Fatalf("open tickets: %+v %v", sum, err)
		}
	})
	// With a subject, the subject wins; a blank one is not a subject.
	conv2, _ := env.SeedConversation(t, env.TenantA, "bot")
	f.exec(`INSERT INTO tickets(tenant_id, conversation_id, subject, provider, external_ticket_id) VALUES($1,$2,'  VPN caiu ','k3g','9')`, env.TenantA, conv2)
	f.sys(env.TenantA, func(ctx context.Context) {
		sum, err := f.effects.OpenTickets(ctx, &ports.ConversationFacts{ContactID: f.contactOf(conv2)})
		if err != nil || sum.FirstSubject != "VPN caiu" {
			t.Fatalf("subject: %+v %v", sum, err)
		}
	})
}
