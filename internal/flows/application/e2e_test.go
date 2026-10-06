package application_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/adapters"
	. "github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/flowstest"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
)

// The whole stack, real: control plane publishes, the engine runs, PostgresEffects + SystemSender act. Only the provider
// is absent (messages stay queued in the outbox, which is exactly where delivery picks them up).
func TestEndToEndLinkDownFlowOnRealEffects(t *testing.T) {
	env := flowstest.New(t)
	repo := adapters.NewPostgresFlowRepository(env.App)
	cp := NewControlPlane(repo, repo, nil)
	effects := adapters.NewPostgresEffects(env.App, messagesapplication.NewSystemSender(messagesadapters.NewPostgresOutboundStore(env.App)))
	eng := NewEngine(repo, repo, effects, AllExecutors())
	tenant := env.TenantA
	ctx := context.Background()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := env.Seed.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	count := func(sql string, args ...any) (n int) {
		t.Helper()
		if err := env.Seed.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return
	}

	noc := uuid.New()
	exec(`INSERT INTO queues(id, tenant_id, name, mode) VALUES($1,$2,'NOC','manual')`, noc, tenant)
	line := uuid.New()
	exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]')`, line, tenant, line.String())
	conv, contact := env.SeedConversation(t, tenant, "bot")
	exec(`UPDATE conversations SET channel_connection_id=$2 WHERE id=$1`, conv, line)
	exec(`INSERT INTO tickets(tenant_id, conversation_id) VALUES($1,$2)`, tenant, conv) // the ingest's placeholder
	acme := uuid.New()
	exec(`INSERT INTO customer_accounts(id, tenant_id, name) VALUES($1,$2,'ACME')`, acme, tenant)
	exec(`INSERT INTO contact_account_links(tenant_id, contact_id, account_id, source, is_primary) VALUES($1,$2,$3,'manual',true)`, tenant, contact, acme)

	def := fmt.Sprintf(`{"schema_version":1,"nodes":[
	  {"id":"start","type":"trigger"},
	  {"id":"hi","type":"send_message","config":{"text":"Olá {{contact.name}}, aqui é o suporte."}},
	  {"id":"who","type":"resolve_customer_context"},
	  {"id":"pick","type":"customer_choice"},
	  {"id":"what","type":"ask","config":{"text":"Qual circuito está fora?","variable":"circuito"}},
	  {"id":"ticket","type":"create_ticket","config":{"subject":"Link down - {{circuito}} ({{customer.name}})","priority":"high"}},
	  {"id":"route","type":"assign_queue","config":{"queue":%q}},
	  {"id":"human","type":"human_handoff","config":{"summary":"{{customer.name}}: {{circuito}}"}},
	  {"id":"bye","type":"end"}],
	 "edges":[
	  {"id":"1","source":"start","sourcePort":"next","target":"hi"},{"id":"2","source":"hi","sourcePort":"next","target":"who"},
	  {"id":"3","source":"who","sourcePort":"none","target":"what"},{"id":"4","source":"who","sourcePort":"single","target":"what"},{"id":"5","source":"who","sourcePort":"multiple","target":"pick"},
	  {"id":"6","source":"pick","sourcePort":"selected","target":"what"},{"id":"7","source":"pick","sourcePort":"timeout","target":"human"},
	  {"id":"8","source":"what","sourcePort":"next","target":"ticket"},{"id":"9","source":"what","sourcePort":"timeout","target":"human"},
	  {"id":"10","source":"ticket","sourcePort":"next","target":"route"},{"id":"11","source":"route","sourcePort":"next","target":"human"}]}`, noc)

	env.AsUser(t, tenant, env.UserA, func(c context.Context) {
		f, err := cp.Create(c, CreateInput{Slug: "link-down", Name: "Link down"})
		if err != nil {
			t.Fatal(err)
		}
		res, err := cp.SaveDraft(c, f.ID, f.DraftRevision, f.Name, "", []byte(def))
		if err != nil || len(res.Issues) > 0 && hasBlocking(res) {
			t.Fatalf("draft: %v %+v", err, res)
		}
		if _, err := cp.Publish(c, f.ID, res.Flow.DraftRevision, ""); err != nil {
			t.Fatalf("publish: %v", err)
		}
	})

	deliver := func(text string, isNew bool) Outcome {
		t.Helper()
		msg := env.SeedInbound(t, tenant, conv, text)
		var out Outcome
		env.AsSystem(t, tenant, func(c context.Context) {
			var err error
			out, err = eng.OnInbound(c, InboundEvent{ConversationID: conv, MessageID: msg, NewConversation: isNew})
			if err != nil {
				t.Fatalf("OnInbound(%q): %v", text, err)
			}
		})
		return out
	}

	if out := deliver("minha internet caiu", true); out != OutcomeStarted {
		t.Fatalf("start: %v", out)
	}
	// greeting + the question, both queued by the SYSTEM sender (no human sender), the contact has exactly one company
	if n := count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound' AND sent_by_user_id IS NULL AND status='queued'`, conv); n != 2 {
		t.Fatalf("expected 2 queued bot messages, got %d", n)
	}
	if out := deliver("circuito POA-123", false); out != OutcomeResumed {
		t.Fatalf("answer: %v", out)
	}

	var status, mode, subject, priority string
	var queue, account *uuid.UUID
	_ = env.Seed.QueryRow(ctx, `SELECT status FROM flow_runs WHERE conversation_id=$1`, conv).Scan(&status)
	var ticketAccount *uuid.UUID
	_ = env.Seed.QueryRow(ctx, `SELECT automation_mode, queue_id FROM conversations WHERE id=$1`, conv).Scan(&mode, &queue)
	_ = env.Seed.QueryRow(ctx, `SELECT active_customer_account_id FROM flow_runs WHERE conversation_id=$1`, conv).Scan(&account)
	_ = env.Seed.QueryRow(ctx, `SELECT subject, priority, customer_account_id FROM tickets WHERE conversation_id=$1`, conv).Scan(&subject, &priority, &ticketAccount)
	if status != "waiting_human" || mode != "waiting_human" {
		t.Fatalf("handoff state: run=%s mode=%s", status, mode)
	}
	if queue == nil || *queue != noc || account == nil || *account != acme || ticketAccount == nil || *ticketAccount != acme {
		t.Fatalf("routing/context: queue=%v run account=%v ticket account=%v (the company lives on the run and the ticket, not the conversation)", queue, account, ticketAccount)
	}
	if subject != "Link down - circuito POA-123 (ACME)" || priority != "high" || count(`SELECT count(*) FROM tickets WHERE conversation_id=$1`, conv) != 1 {
		t.Fatalf("ticket: %q %q", subject, priority)
	}
	var handoff string
	_ = env.Seed.QueryRow(ctx, `SELECT variables->'_handoff'->>'summary' FROM flow_runs WHERE conversation_id=$1`, conv).Scan(&handoff)
	if handoff != "ACME: circuito POA-123" {
		t.Fatalf("operator context: %q", handoff)
	}
	if n := count(`SELECT count(*) FROM flow_node_executions fe JOIN flow_runs r ON r.id=fe.flow_run_id WHERE r.conversation_id=$1`, conv); n != 8 {
		t.Fatalf("audit trail steps: %d", n)
	}

	// After the handoff the bot is silent: a new message creates nothing and sends nothing.
	before := count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, conv)
	if out := deliver("alguém?", false); out != OutcomeIgnored {
		t.Fatalf("after handoff: %v", out)
	}
	if count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, conv) != before || count(`SELECT count(*) FROM flow_runs WHERE conversation_id=$1`, conv) != 1 {
		t.Fatal("the bot reacted after the handoff")
	}
	// An operator claims it: the bot stays silent for good, whatever the flag says.
	op := uuid.New()
	exec(`INSERT INTO users(id, external_subject, email, status) VALUES($1,$2,$3,'active')`, op, op, op.String()+"@invalid")
	t.Cleanup(func() { _, _ = env.Seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, op) })
	exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, conv, op)
	if out := deliver("obrigado", false); out != OutcomeIgnored {
		t.Fatalf("after claim: %v", out)
	}
	_ = strings.TrimSpace
}

func hasBlocking(r *DraftResult) bool {
	for _, i := range r.Issues {
		if i.Severity == "error" {
			return true
		}
	}
	return false
}
