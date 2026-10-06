package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/adapters"
	. "github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/flowstest"
)

type rt struct {
	t      *testing.T
	env    *flowstest.Env
	repo   *adapters.PostgresFlowRepository
	cp     *ControlPlane
	fx     *flowstest.RecordingEffects
	eng    *Engine
	tenant uuid.UUID
	conv   uuid.UUID
	now    time.Time
	mu     sync.Mutex
}

func newRT(t *testing.T) *rt {
	env := flowstest.New(t)
	repo := adapters.NewPostgresFlowRepository(env.App)
	x := &rt{t: t, env: env, repo: repo, cp: NewControlPlane(repo, repo, nil), fx: &flowstest.RecordingEffects{}, tenant: env.TenantA, now: time.Now().UTC()}
	x.eng = NewEngine(repo, repo, x.fx, PureExecutors()).WithClock(func() time.Time { x.mu.Lock(); defer x.mu.Unlock(); return x.now }).WithLogger(func(string, ...any) {})
	x.conv, _ = env.SeedConversation(t, env.TenantA, "bot")
	return x
}

func (x *rt) advance(d time.Duration) { x.mu.Lock(); x.now = x.now.Add(d); x.mu.Unlock() }

// publish creates, saves and publishes a flow through the real control plane and returns its id.
func (x *rt) publish(slug, def string) uuid.UUID {
	x.t.Helper()
	var id uuid.UUID
	x.env.AsUser(x.t, x.tenant, x.env.UserA, func(ctx context.Context) {
		f, err := x.cp.Create(ctx, CreateInput{Slug: slug, Name: slug})
		if err != nil {
			x.t.Fatal(err)
		}
		res, err := x.cp.SaveDraft(ctx, f.ID, f.DraftRevision, f.Name, "", json.RawMessage(def))
		if err != nil || domain.HasErrors(res.Issues) {
			x.t.Fatalf("draft: %v %+v", err, res)
		}
		if _, err := x.cp.Publish(ctx, f.ID, res.Flow.DraftRevision, ""); err != nil {
			x.t.Fatalf("publish: %v", err)
		}
		id = f.ID
	})
	return id
}

// deliver processes one inbound event the way the worker does: one system tenant session = one transaction.
func (x *rt) deliver(messageID uuid.UUID, newConv bool) (out Outcome, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = platformdbSystem(ctx, x.env, x.tenant, func(sc context.Context) error {
		out, err = x.eng.OnInbound(sc, InboundEvent{ConversationID: x.conv, MessageID: messageID, NewConversation: newConv})
		return err
	})
	return out, err
}

func (x *rt) say(text string) uuid.UUID { return x.env.SeedInbound(x.t, x.tenant, x.conv, text) }

const askNameFlow = `{"schema_version":1,"nodes":[
 {"id":"start","type":"trigger"},
 {"id":"q","type":"ask","config":{"text":"Qual o seu nome?","variable":"nome"}},
 {"id":"hi","type":"send_message","config":{"text":"Prazer, {{nome}}! (v1)"}},
 {"id":"bye","type":"end"},{"id":"slow","type":"end"}],
 "edges":[{"id":"1","source":"start","sourcePort":"next","target":"q"},{"id":"2","source":"q","sourcePort":"next","target":"hi"},
  {"id":"3","source":"q","sourcePort":"timeout","target":"slow"},{"id":"4","source":"hi","sourcePort":"next","target":"bye"}]}`

func (x *rt) runStatus() (status, mode string, execs int) {
	ctx := context.Background()
	if err := x.env.Seed.QueryRow(ctx, `SELECT status FROM flow_runs WHERE conversation_id=$1 ORDER BY started_at DESC LIMIT 1`, x.conv).Scan(&status); err != nil {
		x.t.Fatal(err)
	}
	_ = x.env.Seed.QueryRow(ctx, `SELECT automation_mode FROM conversations WHERE id=$1`, x.conv).Scan(&mode)
	_ = x.env.Seed.QueryRow(ctx, `SELECT count(*) FROM flow_node_executions fe JOIN flow_runs r ON r.id=fe.flow_run_id WHERE r.conversation_id=$1`, x.conv).Scan(&execs)
	return
}

func TestRuntimeWaitsAndResumesAcrossTransactions(t *testing.T) {
	x := newRT(t)
	x.publish("ask-name", askNameFlow)
	if out, err := x.deliver(x.say("oi"), true); err != nil || out != OutcomeStarted {
		t.Fatalf("start: %v %v", out, err)
	}
	if st, mode, execs := x.runStatus(); st != "waiting_input" || mode != "bot" || execs != 2 {
		t.Fatalf("after the question: %s %s %d", st, mode, execs)
	}
	if out, err := x.deliver(x.say("Carlos"), false); err != nil || out != OutcomeResumed {
		t.Fatalf("resume: %v %v", out, err)
	}
	st, mode, execs := x.runStatus()
	if st != "completed" || mode != "none" || execs != 5 {
		t.Fatalf("after the answer: %s %s %d", st, mode, execs)
	}
	if got := strings.Join(x.fx.SentTexts(), "|"); got != "Qual o seu nome?|Prazer, Carlos! (v1)" {
		t.Fatalf("messages: %q", got)
	}
	if len(x.fx.Assigned) != 1 {
		t.Fatalf("the finished bot must hand the conversation back to the queue flow: %v", x.fx.Assigned)
	}
	var vars string
	_ = x.env.Seed.QueryRow(context.Background(), `SELECT variables::text FROM flow_runs WHERE conversation_id=$1`, x.conv).Scan(&vars)
	if !strings.Contains(vars, `"nome": "Carlos"`) {
		t.Fatalf("variables not persisted: %s", vars)
	}
}

func TestConcurrentDeliveriesOfOneConversationAreSerialized(t *testing.T) {
	x := newRT(t)
	x.publish("ask-name", askNameFlow)
	if _, err := x.deliver(x.say("oi"), true); err != nil {
		t.Fatal(err)
	}
	// The SAME answer delivered 8 times at once (redelivery storm) plus 8 different late messages.
	answer := x.say("Carlos")
	var wg sync.WaitGroup
	results := make(chan Outcome, 16)
	errs := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			out, err := x.deliver(answer, false)
			results <- out
			errs <- err
		}()
		go func(i int) {
			defer wg.Done()
			out, err := x.deliver(x.say(fmt.Sprintf("extra %d", i)), false)
			results <- out
			errs <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a concurrent delivery failed: %v", err)
		}
	}
	resumed := 0
	for o := range results {
		if o == OutcomeResumed {
			resumed++
		}
	}
	// Exactly one message consumed the question; everything else was a duplicate or ignored.
	if resumed != 1 {
		t.Fatalf("exactly one delivery may resume the run, got %d", resumed)
	}
	var runs, completed int
	_ = x.env.Seed.QueryRow(context.Background(), `SELECT count(*), count(*) FILTER (WHERE status='completed') FROM flow_runs WHERE conversation_id=$1`, x.conv).Scan(&runs, &completed)
	if runs != 1 || completed != 1 {
		t.Fatalf("one run, completed once: runs=%d completed=%d", runs, completed)
	}
	if n := len(x.fx.SentTexts()); n != 2 {
		t.Fatalf("every message must be sent exactly once, got %d: %v", n, x.fx.SentTexts())
	}
	var distinct, total int
	_ = x.env.Seed.QueryRow(context.Background(), `SELECT count(DISTINCT seq), count(*) FROM flow_node_executions WHERE flow_run_id=(SELECT id FROM flow_runs WHERE conversation_id=$1)`, x.conv).Scan(&distinct, &total)
	if distinct != total || total != 5 {
		t.Fatalf("audit trail must have unique, complete steps: %d/%d", distinct, total)
	}
}

func TestOnlyOneActiveRunPerConversationInTheDatabase(t *testing.T) {
	x := newRT(t)
	x.publish("ask-name", askNameFlow)
	if _, err := x.deliver(x.say("oi"), true); err != nil {
		t.Fatal(err)
	}
	var flowID, versionID uuid.UUID
	_ = x.env.Seed.QueryRow(context.Background(), `SELECT flow_id, flow_version_id FROM flow_runs WHERE conversation_id=$1`, x.conv).Scan(&flowID, &versionID)
	twin := &domain.FlowRun{ID: uuid.New(), TenantID: x.tenant, FlowID: flowID, FlowVersionID: versionID, ConversationID: x.conv, Status: domain.RunRunning,
		Variables: map[string]any{}, TriggerEventID: uuid.NewString(), StartedAt: time.Now(), UpdatedAt: time.Now()}
	err := platformdbSystem(context.Background(), x.env, x.tenant, func(sc context.Context) error { return x.repo.CreateRun(sc, twin) })
	if !errors.Is(err, domain.ErrConversationBusy) {
		t.Fatalf("a second active run on the same conversation must be refused by the database: %v", err)
	}
	twin2 := *twin
	twin2.ID = uuid.New()
	var first string
	_ = x.env.Seed.QueryRow(context.Background(), `SELECT trigger_event_id FROM flow_runs WHERE conversation_id=$1`, x.conv).Scan(&first)
	twin2.TriggerEventID = first
	err = platformdbSystem(context.Background(), x.env, x.tenant, func(sc context.Context) error { return x.repo.CreateRun(sc, &twin2) })
	if !errors.Is(err, domain.ErrDuplicateEvent) && !errors.Is(err, domain.ErrConversationBusy) {
		t.Fatalf("the same trigger event twice must be refused: %v", err)
	}
}

func TestRunsAreTenantIsolated(t *testing.T) {
	x := newRT(t)
	x.publish("ask-name", askNameFlow)
	if _, err := x.deliver(x.say("oi"), true); err != nil {
		t.Fatal(err)
	}
	var runID uuid.UUID
	_ = x.env.Seed.QueryRow(context.Background(), `SELECT id FROM flow_runs WHERE conversation_id=$1`, x.conv).Scan(&runID)
	// Tenant B's worker session cannot see tenant A's conversation, run or messages, and cannot advance it.
	err := platformdbSystem(context.Background(), x.env, x.env.TenantB, func(sc context.Context) error {
		if _, err := x.repo.LoadConversation(sc, x.conv); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("tenant B loaded tenant A's conversation: %v", err)
		}
		if _, err := x.repo.GetRun(sc, runID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("tenant B loaded tenant A's run: %v", err)
		}
		if _, err := x.repo.ActiveRun(sc, x.conv); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("tenant B saw tenant A's active run: %v", err)
		}
		if _, err := x.eng.OnTimeout(sc, runID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("tenant B advanced tenant A's run: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunStaysOnItsPinnedVersionWhenAnotherIsPublished(t *testing.T) {
	x := newRT(t)
	flowID := x.publish("ask-name", askNameFlow)
	if _, err := x.deliver(x.say("oi"), true); err != nil {
		t.Fatal(err)
	}
	// While the run waits, a new version with different content is published and becomes active.
	v2 := strings.Replace(askNameFlow, "(v1)", "(v2)", 1)
	x.env.AsUser(t, x.tenant, x.env.UserA, func(ctx context.Context) {
		f, _ := x.cp.Get(ctx, flowID)
		res, err := x.cp.SaveDraft(ctx, flowID, f.DraftRevision, f.Name, "", json.RawMessage(v2))
		if err != nil {
			t.Fatal(err)
		}
		pub, err := x.cp.Publish(ctx, flowID, res.Flow.DraftRevision, "")
		if err != nil || pub.Version.Version != 2 {
			t.Fatalf("publish v2: %+v %v", pub, err)
		}
	})
	if _, err := x.deliver(x.say("Carlos"), false); err != nil {
		t.Fatal(err)
	}
	if got := x.fx.SentTexts(); got[len(got)-1] != "Prazer, Carlos! (v1)" {
		t.Fatalf("an in-flight run must finish on the version it started with: %q", got)
	}
	// A NEW conversation starts on v2.
	x2conv, _ := x.env.SeedConversation(t, x.tenant, "bot")
	x.conv = x2conv
	if _, err := x.deliver(x.say("oi"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := x.deliver(x.say("Dora"), false); err != nil {
		t.Fatal(err)
	}
	if got := x.fx.SentTexts(); got[len(got)-1] != "Prazer, Dora! (v2)" {
		t.Fatalf("new conversations use the active version: %q", got)
	}
}

func TestTimeoutIsDrivenFromTheDatabase(t *testing.T) {
	x := newRT(t)
	x.publish("ask-name", askNameFlow)
	if _, err := x.deliver(x.say("oi"), true); err != nil {
		t.Fatal(err)
	}
	var due []adapters.DueRun
	list := func() {
		_ = platformdbSystemAdmin(context.Background(), x.env, func(sc context.Context) error {
			var err error
			due, err = x.repo.DueRuns(sc, x.now, 10)
			return err
		})
	}
	list()
	if len(due) != 0 {
		t.Fatalf("nothing is due yet: %v", due)
	}
	x.advance(25 * time.Hour) // the default input timeout is 24h
	list()
	if len(due) != 1 || due[0].TenantID != x.tenant {
		t.Fatalf("one run must be due: %v", due)
	}
	err := platformdbSystem(context.Background(), x.env, due[0].TenantID, func(sc context.Context) error {
		out, err := x.eng.OnTimeout(sc, due[0].RunID)
		if out != OutcomeTimedOut {
			t.Errorf("outcome %v", out)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if st, mode, _ := x.runStatus(); st != "completed" || mode != "none" {
		t.Fatalf("after the timeout: %s %s", st, mode)
	}
}

func TestAuditTrailIsRedactedInTheDatabase(t *testing.T) {
	x := newRT(t)
	x.publish("ask-name", askNameFlow)
	if _, err := x.deliver(x.say("oi"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := x.deliver(x.say("Bearer abcdefghijklmnop1234"), false); err != nil {
		t.Fatal(err)
	}
	var leaked int
	_ = x.env.Seed.QueryRow(context.Background(), `SELECT count(*) FROM flow_node_executions WHERE input::text LIKE '%abcdefghijklmnop1234%' OR output::text LIKE '%abcdefghijklmnop1234%'`).Scan(&leaked)
	if leaked != 0 {
		t.Fatalf("%d audit rows contain the secret", leaked)
	}
}
