package adapters_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/adapters"
	"github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

type gateEnv struct {
	*fx
	repo *adapters.PostgresFlowRepository
	cp   *application.ControlPlane
	gate *adapters.Gate
}

func newGateEnv(t *testing.T) *gateEnv {
	f := newFx(t)
	repo := adapters.NewPostgresFlowRepository(f.env.App)
	g := adapters.NewGate(f.env.App, repo)
	return &gateEnv{fx: f, repo: repo, cp: application.NewControlPlane(repo, repo, nil), gate: g}
}

const trivialFlow = `{"schema_version":1,"nodes":[{"id":"start","type":"trigger"},{"id":"e","type":"end"}],"edges":[{"id":"1","source":"start","sourcePort":"next","target":"e"}]}`

// publish creates and publishes a flow in tenant A with optional settings.
func (g *gateEnv) publish(slug string, s *ports.Settings) {
	g.t.Helper()
	g.env.AsUser(g.t, g.env.TenantA, g.env.UserA, func(ctx context.Context) {
		f, err := g.cp.Create(ctx, application.CreateInput{Slug: slug, Name: slug})
		if err != nil {
			g.t.Fatal(err)
		}
		res, err := g.cp.SaveDraft(ctx, f.ID, f.DraftRevision, f.Name, "", json.RawMessage(trivialFlow))
		if err != nil {
			g.t.Fatal(err)
		}
		if s != nil {
			if _, err := g.cp.UpdateSettings(ctx, f.ID, *s); err != nil {
				g.t.Fatal(err)
			}
		}
		if _, err := g.cp.Publish(ctx, f.ID, res.Flow.DraftRevision, ""); err != nil {
			g.t.Fatal(err)
		}
	})
}

func (g *gateEnv) mode(conv uuid.UUID) (m string) {
	_ = g.env.Seed.QueryRow(context.Background(), `SELECT automation_mode FROM conversations WHERE id=$1`, conv).Scan(&m)
	return
}

func TestGateEngageHoldsOnlyWhatAFlowCanTake(t *testing.T) {
	g := newGateEnv(t)
	env := g.env
	engage := func(conv uuid.UUID) (held bool) {
		g.sys(env.TenantA, func(ctx context.Context) { held = g.gate.Engage(ctx, conv) })
		return
	}
	conv, _ := env.SeedConversation(t, env.TenantA, "none")
	if engage(conv) || g.mode(conv) != "none" {
		t.Fatal("no published flow: the legacy routing must stay in charge")
	}
	g.publish("reception", nil)
	if !engage(conv) || g.mode(conv) != "bot" {
		t.Fatal("a published flow must take a new conversation of an unclassified contact")
	}
	// Not startable: a conversation with staff, an assigned one, one with an identity conflict.
	internal, _ := env.SeedConversation(t, env.TenantA, "none")
	g.exec(`UPDATE conversations SET conversation_kind='external_other' WHERE id=$1`, internal)
	assigned, _ := env.SeedConversation(t, env.TenantA, "none")
	op := uuid.New()
	g.exec(`INSERT INTO users(id, external_subject, email, status) VALUES($1,$2,$3,'active')`, op, op, op.String()+"@invalid")
	t.Cleanup(func() { _, _ = env.Seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, op) })
	g.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, assigned, op)
	conflict, conflictContact := env.SeedConversation(t, env.TenantA, "none")
	identity := uuid.New()
	g.exec(`INSERT INTO user_channel_identities(id, tenant_id, user_id, identity_type, raw_value, normalized_value) VALUES($1,$2,$3,'phone','+5511988887777','+5511988887777')`, identity, env.TenantA, env.UserA)
	g.exec(`INSERT INTO identity_resolution_conflicts(tenant_id, identity_id, contact_id) VALUES($1,$2,$3)`, env.TenantA, identity, conflictContact)
	for name, c := range map[string]uuid.UUID{"not a customer conversation": internal, "assigned": assigned, "open identity conflict": conflict} {
		if engage(c) || g.mode(c) != "none" {
			t.Errorf("%s must not be held", name)
		}
	}
	// A flow scoped to another channel line does not take this conversation.
	g2 := newGateEnv(t)
	line, otherLine := uuid.New(), uuid.New()
	for _, l := range []uuid.UUID{line, otherLine} {
		g2.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]')`, l, g2.env.TenantA, l.String())
	}
	g2.publish("only-other-line", &ports.Settings{Priority: 10, RestartPolicy: domain.RestartNewConversationOnly, TriggerFilter: domain.TriggerFilter{ConnectionIDs: []uuid.UUID{otherLine}}})
	c2, _ := g2.env.SeedConversation(t, g2.env.TenantA, "none")
	g2.exec(`UPDATE conversations SET channel_connection_id=$2 WHERE id=$1`, c2, line)
	var held bool
	g2.sys(g2.env.TenantA, func(ctx context.Context) { held = g2.gate.Engage(ctx, c2) })
	if held {
		t.Fatal("a flow bound to another channel line must not take this one")
	}
	g2.exec(`UPDATE conversations SET channel_connection_id=$2 WHERE id=$1`, c2, otherLine)
	g2.sys(g2.env.TenantA, func(ctx context.Context) { held = g2.gate.Engage(ctx, c2) })
	if !held {
		t.Fatal("the flow's own line must be held")
	}
}

func TestGateOnInboundEnqueuesReferencesOnlyAndOnlyWhenRelevant(t *testing.T) {
	g := newGateEnv(t)
	env := g.env
	g.publish("reception", nil)
	bot, _ := env.SeedConversation(t, env.TenantA, "bot")
	legacy, _ := env.SeedConversation(t, env.TenantA, "none")
	msgBot := env.SeedInbound(t, env.TenantA, bot, "minha senha é hunter2")
	msgLegacy := env.SeedInbound(t, env.TenantA, legacy, "oi")
	g.sys(env.TenantA, func(ctx context.Context) {
		g.gate.OnInbound(ctx, bot, msgBot, true)
		g.gate.OnInbound(ctx, legacy, msgLegacy, false)
	})
	if n := g.count(`SELECT count(*) FROM outbox_events WHERE event_type=$1 AND aggregate_id=$2`, adapters.JobFlowInbound, bot.String()); n != 1 {
		t.Fatalf("a bot-held conversation must enqueue one job: %d", n)
	}
	if n := g.count(`SELECT count(*) FROM outbox_events WHERE event_type=$1 AND aggregate_id=$2`, adapters.JobFlowInbound, legacy.String()); n != 0 {
		t.Fatalf("a legacy conversation (and no restart-always flow) must enqueue nothing: %d", n)
	}
	var payload string
	_ = env.Seed.QueryRow(context.Background(), `SELECT payload::text FROM outbox_events WHERE event_type=$1 AND aggregate_id=$2`, adapters.JobFlowInbound, bot.String()).Scan(&payload)
	if strings.Contains(payload, "hunter2") || strings.Contains(payload, "senha") || strings.Contains(payload, "tenant") || !strings.Contains(payload, msgBot.String()) {
		t.Fatalf("the job carries the message id only, never text or tenant: %s", payload)
	}
	// With a restart-always flow published, follow-ups of legacy conversations are enqueued too.
	g.publish("always", &ports.Settings{Priority: 20, RestartPolicy: domain.RestartAlways})
	g.sys(env.TenantA, func(ctx context.Context) { g.gate.OnInbound(ctx, legacy, msgLegacy, false) })
	if n := g.count(`SELECT count(*) FROM outbox_events WHERE event_type=$1 AND aggregate_id=$2`, adapters.JobFlowInbound, legacy.String()); n != 1 {
		t.Fatalf("restart_policy=always must enqueue: %d", n)
	}
	// ...but only conversations a flow could actually start: not assigned, not closed, not a staff/other conversation.
	op := uuid.New()
	g.exec(`INSERT INTO users(id, external_subject, email, status) VALUES($1,$2,$3,'active')`, op, op, op.String()+"@invalid")
	t.Cleanup(func() { _, _ = env.Seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, op) })
	for name, set := range map[string]string{
		"assigned to a human": `assigned_to_user_id='` + op.String() + `'`,
		"closed":              `status='closed'`,
		"not a customer":      `conversation_kind='external_other'`,
	} {
		c, _ := env.SeedConversation(t, env.TenantA, "none")
		g.exec(`UPDATE conversations SET `+set+` WHERE id=$1`, c)
		m := env.SeedInbound(t, env.TenantA, c, "oi")
		g.sys(env.TenantA, func(ctx context.Context) { g.gate.OnInbound(ctx, c, m, false) })
		if n := g.count(`SELECT count(*) FROM outbox_events WHERE event_type=$1 AND aggregate_id=$2`, adapters.JobFlowInbound, c.String()); n != 0 {
			t.Errorf("%s: a restart-always flow must not make the gate enqueue a job the engine would discard (%d)", name, n)
		}
	}
}

// A flow's active version must belong to THAT flow: the pointer is a composite FK on (tenant, flow, version).
func TestActiveVersionMustBelongToItsOwnFlow(t *testing.T) {
	g := newGateEnv(t)
	g.publish("flow-a", nil)
	g.publish("flow-b", nil)
	var aID, bVersion uuid.UUID
	if err := g.env.Seed.QueryRow(context.Background(), `SELECT id FROM flows WHERE tenant_id=$1 AND slug='flow-a'`, g.env.TenantA).Scan(&aID); err != nil {
		t.Fatal(err)
	}
	if err := g.env.Seed.QueryRow(context.Background(), `SELECT v.id FROM flow_versions v JOIN flows f ON f.id=v.flow_id WHERE f.tenant_id=$1 AND f.slug='flow-b'`, g.env.TenantA).Scan(&bVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := g.env.Seed.Exec(context.Background(), `UPDATE flows SET active_version_id=$2 WHERE id=$1`, aID, bVersion); err == nil {
		t.Fatal("pointing flow A at a version of flow B must be refused by the database, even for the owner role")
	}
}

func TestGateNeverBreaksTheIngestTransaction(t *testing.T) {
	g := newGateEnv(t)
	env := g.env
	g.publish("reception", nil)
	foreign, _ := env.SeedConversation(t, env.TenantB, "none")
	g.sys(env.TenantA, func(ctx context.Context) {
		// A conversation that is not this tenant's: the gate swallows the failure and answers "not held"...
		if g.gate.Engage(ctx, foreign) {
			t.Error("a foreign conversation must never be held")
		}
		// (a conversation of another tenant is simply not relevant: nothing is enqueued and that is not a failure)
		if !g.gate.OnInbound(ctx, foreign, uuid.New(), true) {
			t.Error("nothing to enqueue is not a failure")
		}
		if n := g.count(`SELECT count(*) FROM outbox_events WHERE event_type=$1 AND aggregate_id=$2`, adapters.JobFlowInbound, foreign.String()); n != 0 {
			t.Errorf("a foreign conversation must never get a job: %d", n)
		}
		if g.gate.OnInbound(context.Background(), uuid.New(), uuid.New(), true) { // no tenant context at all
			t.Error("a failed enqueue (here: no tenant context) must be reported so the ingest can give the conversation back")
		}
		// ... and the surrounding transaction is still perfectly usable afterwards (savepoint rolled back, not aborted).
		var one int
		if err := platformdb.QuerierFromContext(ctx, g.env.App).QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
			t.Fatalf("the ingest transaction was poisoned by the gate: %v", err)
		}
	})
}

// FLOW-201: Release takes back the hold of a new conversation, and only a bot hold.
func TestGateReleaseGivesBackABotHeldConversation(t *testing.T) {
	g := newGateEnv(t)
	env := g.env
	g.publish("reception", nil)
	conv, _ := env.SeedConversation(t, env.TenantA, "none")
	var held bool
	g.sys(env.TenantA, func(ctx context.Context) { held = g.gate.Engage(ctx, conv) })
	if !held || g.mode(conv) != "bot" {
		t.Fatalf("setup: the flow must hold the new conversation (mode %s)", g.mode(conv))
	}
	g.sys(env.TenantA, func(ctx context.Context) { g.gate.Release(ctx, conv) })
	if g.mode(conv) != "none" {
		t.Fatalf("Release must return the conversation to the normal routing: mode=%s", g.mode(conv))
	}
	// A successful enqueue reports true.
	m := env.SeedInbound(t, env.TenantA, conv, "oi")
	var ok bool
	g.sys(env.TenantA, func(ctx context.Context) { g.gate.Engage(ctx, conv); ok = g.gate.OnInbound(ctx, conv, m, true) })
	if !ok {
		t.Fatal("a successful enqueue must report true")
	}
}

// Migration 000087: the tables documented as append-only really are, for the application role (000006 grants everything by default).
func TestAppendOnlyFlowTablesRefuseUpdateAndDeleteToTheAppRole(t *testing.T) {
	g := newGateEnv(t)
	for _, table := range []string{"flow_versions", "flow_node_executions", "flow_pack_installations", "flow_template_installations"} {
		for _, stmt := range []string{`UPDATE ` + table + ` SET tenant_id = tenant_id WHERE false`, `DELETE FROM ` + table + ` WHERE false`} {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			conn, err := g.env.App.Acquire(ctx)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			_, err = conn.Exec(ctx, stmt)
			conn.Release()
			cancel()
			if err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Errorf("%s must be denied to the application role: %v", stmt, err)
			}
		}
	}
}
