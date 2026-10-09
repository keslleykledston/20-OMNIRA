package flows_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/adapters"
	"github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/flowstest"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	workerflows "github.com/omnira/omnira/internal/worker/flows"
)

type stack struct {
	t       *testing.T
	env     *flowstest.Env
	repo    *adapters.PostgresFlowRepository
	gate    *adapters.Gate
	engine  *application.Engine
	handler *workerflows.Handler
	sweeper *workerflows.Sweeper
	clock   time.Time
	conv    uuid.UUID
}

func newStack(t *testing.T) *stack {
	env := flowstest.New(t)
	repo := adapters.NewPostgresFlowRepository(env.App)
	effects := adapters.NewPostgresEffects(env.App, messagesapplication.NewSystemSender(messagesadapters.NewPostgresOutboundStore(env.App)))
	s := &stack{t: t, env: env, repo: repo, gate: adapters.NewGate(env.App, repo), clock: time.Now().UTC()}
	s.engine = application.NewEngine(repo, repo, effects, application.AllExecutors()).WithClock(func() time.Time { return s.clock }).WithLogger(func(string, ...any) {})
	h, err := workerflows.NewHandler(workerflows.NewPostgresConversationRunner(env.App), s.engine)
	if err != nil {
		t.Fatal(err)
	}
	s.handler = h
	s.sweeper = workerflows.NewSweeper(env.App, repo, s.engine).WithClock(func() time.Time { return s.clock })
	// a usable text line and a default queue
	line := uuid.New()
	s.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]')`, line, env.TenantA, line.String())
	s.exec(`INSERT INTO queues(tenant_id, name, mode, is_default) VALUES($1,'default','manual',true)`, env.TenantA)
	s.conv, _ = env.SeedConversation(t, env.TenantA, "none")
	s.exec(`UPDATE conversations SET channel_connection_id=$2 WHERE id=$1`, s.conv, line)
	return s
}

func (s *stack) exec(sql string, args ...any) {
	s.t.Helper()
	if _, err := s.env.Seed.Exec(context.Background(), sql, args...); err != nil {
		s.t.Fatalf("%v\n%s", err, sql)
	}
}

func (s *stack) count(sql string, args ...any) (n int) {
	s.t.Helper()
	if err := s.env.Seed.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return
}

const askFlow = `{"schema_version":1,"nodes":[{"id":"start","type":"trigger"},
 {"id":"q","type":"ask","config":{"text":"Qual o seu nome?","variable":"nome"}},{"id":"hi","type":"send_message","config":{"text":"Prazer, {{nome}}!"}},
 {"id":"bye","type":"end"},{"id":"slow","type":"end"}],
 "edges":[{"id":"1","source":"start","sourcePort":"next","target":"q"},{"id":"2","source":"q","sourcePort":"next","target":"hi"},{"id":"3","source":"q","sourcePort":"timeout","target":"slow"},{"id":"4","source":"hi","sourcePort":"next","target":"bye"}]}`

func (s *stack) publish() {
	cp := application.NewControlPlane(s.repo, s.repo, nil)
	s.env.AsUser(s.t, s.env.TenantA, s.env.UserA, func(ctx context.Context) {
		f, err := cp.Create(ctx, application.CreateInput{Slug: "ask-name", Name: "Ask name"})
		if err != nil {
			s.t.Fatal(err)
		}
		res, err := cp.SaveDraft(ctx, f.ID, f.DraftRevision, f.Name, "", json.RawMessage(askFlow))
		if err != nil {
			s.t.Fatal(err)
		}
		if _, err := cp.Publish(ctx, f.ID, res.Flow.DraftRevision, ""); err != nil {
			s.t.Fatal(err)
		}
	})
}

// ingest does what the inbound pipeline does inside ONE tenant transaction: store the message, ask the gate whether a flow
// holds the conversation (new conversations only), and tell it about the message.
func (s *stack) ingest(text string, newConv bool) (messageID uuid.UUID) {
	s.t.Helper()
	messageID = s.env.SeedInbound(s.t, s.env.TenantA, s.conv, text)
	s.env.AsSystem(s.t, s.env.TenantA, func(ctx context.Context) {
		if newConv {
			s.gate.Engage(ctx, s.conv)
		}
		s.gate.OnInbound(ctx, s.conv, messageID, newConv)
	})
	return messageID
}

// envelopes returns the queued jobs exactly as the outbox publisher serialises them for JetStream.
func (s *stack) envelopes() [][]byte {
	rows, err := s.env.Seed.Query(context.Background(), `SELECT id, tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, payload, created_at FROM outbox_events
		WHERE event_type='job.flow.inbound.v1' AND aggregate_id=$1 ORDER BY created_at`, s.conv.String())
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var id, tenant uuid.UUID
		var et, at, agg string
		var corr uuid.UUID
		var payload []byte
		var created time.Time
		if err := rows.Scan(&id, &tenant, &et, &at, &agg, &corr, &payload, &created); err != nil {
			s.t.Fatal(err)
		}
		b, _ := json.Marshal(map[string]any{"id": id.String(), "tenant_id": tenant.String(), "event_type": et, "aggregate_type": at, "aggregate_id": agg,
			"correlation_id": corr.String(), "causation_id": uuid.Nil.String(), "payload": json.RawMessage(payload), "timestamp": created.Format(time.RFC3339)})
		out = append(out, b)
	}
	return out
}

func (s *stack) runStatus() (st string) {
	_ = s.env.Seed.QueryRow(context.Background(), `SELECT status FROM flow_runs WHERE conversation_id=$1 ORDER BY started_at DESC LIMIT 1`, s.conv).Scan(&st)
	return
}

func TestInboundJobDrivesTheFlowAndRedeliveryIsHarmless(t *testing.T) {
	s := newStack(t)
	s.publish()
	s.ingest("oi", true)
	jobs := s.envelopes()
	if len(jobs) != 1 {
		t.Fatalf("one job expected, got %d", len(jobs))
	}
	if err := s.handler.Handle(context.Background(), jobs[0]); err != nil {
		t.Fatal(err)
	}
	if s.runStatus() != "waiting_input" || s.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, s.conv) != 1 {
		t.Fatalf("the first job must start the run and ask: %s", s.runStatus())
	}
	// JetStream redelivers the same job (ack lost): nothing new happens.
	for i := 0; i < 3; i++ {
		if err := s.handler.Handle(context.Background(), jobs[0]); err != nil {
			t.Fatalf("redelivery must be harmless: %v", err)
		}
	}
	if s.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, s.conv) != 1 || s.count(`SELECT count(*) FROM flow_runs WHERE conversation_id=$1`, s.conv) != 1 {
		t.Fatal("redelivery duplicated work")
	}
	// The contact answers: a second job resumes the same run to completion.
	s.ingest("Carlos", false)
	jobs = s.envelopes()
	if len(jobs) != 2 {
		t.Fatalf("two jobs expected: %d", len(jobs))
	}
	if err := s.handler.Handle(context.Background(), jobs[1]); err != nil {
		t.Fatal(err)
	}
	if s.runStatus() != "completed" || s.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, s.conv) != 2 {
		t.Fatalf("the answer must complete the run: %s", s.runStatus())
	}
	var mode string
	_ = s.env.Seed.QueryRow(context.Background(), `SELECT automation_mode FROM conversations WHERE id=$1`, s.conv).Scan(&mode)
	if mode != "none" || s.count(`SELECT count(*) FROM conversations WHERE id=$1 AND queue_id IS NOT NULL`, s.conv) != 1 {
		t.Fatalf("a finished bot returns the conversation to the default queue: mode=%s", mode)
	}
}

func TestHandlerTrustsOnlyPersistedState(t *testing.T) {
	s := newStack(t)
	s.publish()
	s.ingest("oi", true)
	job := s.envelopes()[0]
	// An envelope claiming ANOTHER tenant is processed under the conversation's real tenant (the claim is ignored).
	var env map[string]any
	_ = json.Unmarshal(job, &env)
	env["tenant_id"] = s.env.TenantB.String()
	forged, _ := json.Marshal(env)
	if err := s.handler.Handle(context.Background(), forged); err != nil {
		t.Fatal(err)
	}
	if s.count(`SELECT count(*) FROM flow_runs WHERE conversation_id=$1 AND tenant_id=$2`, s.conv, s.env.TenantA) != 1 || s.count(`SELECT count(*) FROM flow_runs WHERE tenant_id=$1`, s.env.TenantB) != 0 {
		t.Fatal("the run must belong to the conversation's tenant, whatever the envelope says")
	}
	// Permanent errors are terminated, not retried.
	for name, raw := range map[string]string{
		"malformed":            `{{{`,
		"no conversation":      `{"aggregate_id":"","payload":{"message_id":"` + uuid.NewString() + `"}}`,
		"no message":           `{"aggregate_id":"` + s.conv.String() + `","payload":{}}`,
		"unknown conversation": `{"aggregate_id":"` + uuid.NewString() + `","payload":{"message_id":"` + uuid.NewString() + `"}}`,
	} {
		err := s.handler.Handle(context.Background(), []byte(raw))
		if !errors.Is(err, workerflows.ErrPermanent) || workerflows.AckAction(err) != workerflows.AckActionTerm {
			t.Errorf("%s must be a permanent error: %v", name, err)
		}
	}
	if workerflows.AckAction(nil) != workerflows.AckActionAck || workerflows.AckAction(fmt.Errorf("db down")) != workerflows.AckActionNak {
		t.Fatal("ack classification")
	}
}

func TestSweeperTimeoutsStrandedAndClosedConversations(t *testing.T) {
	s := newStack(t)
	s.publish()
	s.ingest("oi", true)
	if err := s.handler.Handle(context.Background(), s.envelopes()[0]); err != nil {
		t.Fatal(err)
	}
	if r := s.sweeper.Tick(context.Background()); r != (workerflows.SweepResult{}) {
		t.Fatalf("nothing is due yet: %+v", r)
	}
	// 1. the wait expires: the timeout port fires and the conversation returns to the queues
	s.clock = s.clock.Add(25 * time.Hour)
	if r := s.sweeper.Tick(context.Background()); r.TimedOut != 1 {
		t.Fatalf("timeout: %+v", r)
	}
	if s.runStatus() != "completed" {
		t.Fatalf("after the timeout: %s", s.runStatus())
	}
	// 2. a conversation the bot holds with no run (its start job was lost) is released after the grace period, not before
	stranded, _ := s.env.SeedConversation(t, s.env.TenantA, "bot")
	s.exec(`UPDATE conversations SET updated_at=$2 WHERE id=$1`, stranded, s.clock.Add(-30*time.Second))
	if r := s.sweeper.Tick(context.Background()); r.Released != 0 {
		t.Fatalf("a fresh hold is not stranded yet: %+v", r)
	}
	s.exec(`UPDATE conversations SET updated_at=$2 WHERE id=$1`, stranded, s.clock.Add(-10*time.Minute))
	if r := s.sweeper.Tick(context.Background()); r.Released != 1 {
		t.Fatalf("release: %+v", r)
	}
	if s.count(`SELECT count(*) FROM conversations WHERE id=$1 AND automation_mode='none' AND queue_id IS NOT NULL`, stranded) != 1 {
		t.Fatal("a released conversation goes back to the normal queue flow")
	}
	// 3. a run waiting for a human whose conversation was closed frees its slot
	closed, _ := s.env.SeedConversation(t, s.env.TenantA, "waiting_human")
	var flowID, versionID uuid.UUID
	_ = s.env.Seed.QueryRow(context.Background(), `SELECT flow_id, flow_version_id FROM flow_runs LIMIT 1`).Scan(&flowID, &versionID)
	s.exec(`INSERT INTO flow_runs(tenant_id, flow_id, flow_version_id, conversation_id, status, trigger_event_id) VALUES($1,$2,$3,$4,'waiting_human',$5)`, s.env.TenantA, flowID, versionID, closed, uuid.NewString())
	s.exec(`UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, closed)
	if r := s.sweeper.Tick(context.Background()); r.Cancelled != 1 {
		t.Fatalf("cancel: %+v", r)
	}
	if s.count(`SELECT count(*) FROM flow_runs WHERE conversation_id=$1 AND status='cancelled'`, closed) != 1 {
		t.Fatal("the run of a closed conversation must be cancelled")
	}
}

// Codex H-03 / ADR-0038: the flows of a suspended company neither start nor advance (no answer goes out, no timeout
// fires), and nothing is lost: the very same job and the same wait run normally after the reactivation.
func TestSuspendedCompanysFlowsNeitherStartNorAdvance(t *testing.T) {
	s := newStack(t)
	s.publish()
	s.ingest("oi", true)
	job := s.envelopes()[0]
	outbound := func() int {
		return s.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, s.conv)
	}
	suspend := func() { s.exec(`UPDATE tenants SET status='suspended' WHERE id=$1`, s.env.TenantA) }
	reactivate := func() { s.exec(`UPDATE tenants SET status='active' WHERE id=$1`, s.env.TenantA) }

	suspend()
	if err := s.handler.Handle(context.Background(), job); err != nil {
		t.Fatalf("a suspended company's job is simply done, not an error: %v", err)
	}
	if s.count(`SELECT count(*) FROM flow_runs WHERE conversation_id=$1`, s.conv) != 0 || outbound() != 0 {
		t.Fatal("a flow started for a suspended company")
	}
	reactivate()
	if err := s.handler.Handle(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if s.runStatus() != "waiting_input" || outbound() != 1 {
		t.Fatalf("after the reactivation the same job must start the flow: %s", s.runStatus())
	}

	// a wait that expires while the company is suspended does not fire; it fires after the reactivation
	s.clock = s.clock.Add(25 * time.Hour)
	suspend()
	if r := s.sweeper.Tick(context.Background()); r.TimedOut != 0 || s.runStatus() != "waiting_input" {
		t.Fatalf("a timeout fired for a suspended company: %+v status=%s", r, s.runStatus())
	}
	reactivate()
	if r := s.sweeper.Tick(context.Background()); r.TimedOut != 1 || s.runStatus() != "completed" {
		t.Fatalf("after the reactivation the timeout must fire: %+v status=%s", r, s.runStatus())
	}
}

// Codex M / ADR-0038: the sweeper does not even tidy the runs of a suspended company (a run of a closed conversation is
// cancelled only after the reactivation).
func TestSweeperDoesNotCancelRunsOfASuspendedCompany(t *testing.T) {
	s := newStack(t)
	s.publish()
	s.ingest("oi", true)
	if err := s.handler.Handle(context.Background(), s.envelopes()[0]); err != nil {
		t.Fatal(err)
	}
	s.exec(`UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, s.conv)
	s.exec(`UPDATE tenants SET status='suspended' WHERE id=$1`, s.env.TenantA)
	if r := s.sweeper.Tick(context.Background()); r.Cancelled != 0 || s.runStatus() != "waiting_input" {
		t.Fatalf("a suspended company's run was cancelled: %+v status=%s", r, s.runStatus())
	}
	s.exec(`UPDATE tenants SET status='active' WHERE id=$1`, s.env.TenantA)
	if r := s.sweeper.Tick(context.Background()); r.Cancelled != 1 || s.runStatus() != "cancelled" {
		t.Fatalf("after the reactivation the run must be cancelled: %+v status=%s", r, s.runStatus())
	}
}

// Codex M / ADR-0038: the run-tidying statement WAITS for a suspension that is in flight (it share-locks the active
// companies it would touch) and, once the company is suspended, cancels nothing.
func TestSweeperWaitsForASuspensionInFlightThenCancelsNothing(t *testing.T) {
	s := newStack(t)
	s.publish()
	s.ingest("oi", true)
	if err := s.handler.Handle(context.Background(), s.envelopes()[0]); err != nil {
		t.Fatal(err)
	}
	s.exec(`UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, s.conv)
	ctx := context.Background()
	tx, err := s.env.Seed.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE tenants SET status='suspended' WHERE id=$1`, s.env.TenantA); err != nil {
		t.Fatal(err)
	}
	done := make(chan workerflows.SweepResult, 1)
	go func() { done <- s.sweeper.Tick(ctx) }()
	select {
	case r := <-done:
		t.Fatalf("the sweep did not wait for the suspension in flight: %+v", r)
	case <-time.After(1500 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.Cancelled != 0 || s.runStatus() != "waiting_input" {
			t.Fatalf("a run of the company suspended meanwhile was cancelled: %+v status=%s", r, s.runStatus())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the sweep never finished")
	}
}
