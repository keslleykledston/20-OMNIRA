package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	. "github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ---- in-memory fakes ---------------------------------------------------------------------------------------------

type memRuns struct {
	conv       map[uuid.UUID]*ports.ConversationFacts
	msgs       map[uuid.UUID]*ports.InboundMessage
	runs       map[uuid.UUID]*domain.FlowRun
	execs      []*domain.NodeExecution
	candidates []*domain.Flow
}

func (m *memRuns) LoadConversation(_ context.Context, id uuid.UUID) (*ports.ConversationFacts, error) {
	if c, ok := m.conv[id]; ok {
		return c, nil
	}
	return nil, domain.ErrNotFound
}
func (m *memRuns) LoadInboundMessage(_ context.Context, _, id uuid.UUID) (*ports.InboundMessage, error) {
	if x, ok := m.msgs[id]; ok {
		return x, nil
	}
	return nil, domain.ErrNotFound
}
func (m *memRuns) RunByEvent(_ context.Context, ev string) (*domain.FlowRun, error) {
	for _, r := range m.runs {
		if r.TriggerEventID == ev || r.LastEventID == ev {
			return r, nil
		}
	}
	return nil, domain.ErrNotFound
}
func (m *memRuns) ActiveRun(_ context.Context, conv uuid.UUID) (*domain.FlowRun, error) {
	for _, r := range m.runs {
		if r.ConversationID == conv && r.Status.Active() {
			return r, nil
		}
	}
	return nil, domain.ErrNotFound
}
func (m *memRuns) GetRun(_ context.Context, id uuid.UUID) (*domain.FlowRun, error) {
	if r, ok := m.runs[id]; ok {
		return r, nil
	}
	return nil, domain.ErrNotFound
}
func (m *memRuns) CandidateFlows(context.Context) ([]*domain.Flow, error) { return m.candidates, nil }
func (m *memRuns) CreateRun(_ context.Context, r *domain.FlowRun) error {
	for _, x := range m.runs {
		if x.TriggerEventID == r.TriggerEventID {
			return domain.ErrDuplicateEvent
		}
		if x.ConversationID == r.ConversationID && x.Status.Active() {
			return domain.ErrConversationBusy
		}
	}
	m.runs[r.ID] = r
	return nil
}
func (m *memRuns) SaveRun(_ context.Context, r *domain.FlowRun) error { m.runs[r.ID] = r; return nil }
func (m *memRuns) AppendExecution(_ context.Context, e *domain.NodeExecution) error {
	m.execs = append(m.execs, e)
	return nil
}
func (m *memRuns) SetAutomationMode(_ context.Context, id uuid.UUID, mode domain.AutomationMode) error {
	m.conv[id].AutomationMode = mode
	return nil
}

type memVersions map[uuid.UUID]*domain.FlowVersion

func (v memVersions) GetVersion(_ context.Context, id uuid.UUID) (*domain.FlowVersion, error) {
	if x, ok := v[id]; ok {
		return x, nil
	}
	return nil, domain.ErrNoSuchVersion
}

type fakeEffects struct {
	sent      []string
	keys      map[string]bool
	status    ports.SendStatus
	assigned  []*uuid.UUID
	handoffs  int
	sendError error
}

func (f *fakeEffects) CustomerCandidates(context.Context, uuid.UUID) ([]ports.CustomerCandidate, error) {
	return nil, nil
}
func (f *fakeEffects) SetActiveCustomer(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (f *fakeEffects) OpenTickets(context.Context, *ports.ConversationFacts) (ports.TicketSummary, error) {
	return ports.TicketSummary{}, nil
}
func (f *fakeEffects) EnsureTicket(context.Context, uuid.UUID, string, string) (uuid.UUID, bool, error) {
	return uuid.New(), true, nil
}
func (f *fakeEffects) AssignQueue(_ context.Context, _ uuid.UUID, q *uuid.UUID) error {
	f.assigned = append(f.assigned, q)
	return nil
}
func (f *fakeEffects) Handoff(context.Context, uuid.UUID, *uuid.UUID) error { f.handoffs++; return nil }
func (f *fakeEffects) SendText(_ context.Context, _ uuid.UUID, text, key string) (ports.SendStatus, error) {
	if f.sendError != nil {
		return "", f.sendError
	}
	if f.status == ports.SendWindowClosed || f.status == ports.SendNoChannel {
		return f.status, nil
	}
	if f.keys == nil {
		f.keys = map[string]bool{}
	}
	if f.keys[key] {
		return ports.SendReplayed, nil
	}
	f.keys[key] = true
	f.sent = append(f.sent, text)
	return ports.SendQueued, nil
}

// ---- world -------------------------------------------------------------------------------------------------------

type world struct {
	t    *testing.T
	runs *memRuns
	fx   *fakeEffects
	eng  *Engine
	ctx  context.Context
	conv *ports.ConversationFacts
	now  time.Time
	ver  memVersions
	logs []string
}

func newWorld(t *testing.T, flows ...flowSpec) *world {
	tenant := uuid.New()
	conv := &ports.ConversationFacts{ID: uuid.New(), TenantID: tenant, Kind: "customer_service", Status: "open", AutomationMode: domain.AutomationBot,
		ContactID: ptr(uuid.New()), ContactName: "Ana", ContactPhone: "+5511999999999", ContactKind: "customer", Provider: "waha"}
	w := &world{t: t, now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC), conv: conv, ver: memVersions{}, fx: &fakeEffects{},
		runs: &memRuns{conv: map[uuid.UUID]*ports.ConversationFacts{conv.ID: conv}, msgs: map[uuid.UUID]*ports.InboundMessage{}, runs: map[uuid.UUID]*domain.FlowRun{}}}
	for _, f := range flows {
		w.addFlow(f)
	}
	tc, _ := tenancydomain.NewTenantContext(tenant, uuid.Nil, tenancydomain.AccessSourceSystem)
	w.ctx = tenancydomain.WithTenantContext(context.Background(), tc)
	w.eng = NewEngine(w.runs, w.ver, w.fx, append(PureExecutors(), failExec{})).WithClock(func() time.Time { return w.now }).WithLogger(func(f string, a ...any) { w.logs = append(w.logs, fmt.Sprintf(f, a...)) })
	return w
}

func ptr[T any](v T) *T { return &v }

type flowSpec struct {
	slug     string
	def      string
	priority int
	policy   domain.RestartPolicy
	filter   domain.TriggerFilter
	isSub    bool
	pins     map[string]uuid.UUID
}

func (w *world) addFlow(s flowSpec) *domain.FlowVersion {
	flow, _ := domain.NewFlow(w.conv.TenantID, s.slug, s.slug, domain.FlowTypeInbound, nil)
	if s.isSub {
		flow.Type = domain.FlowTypeSubflow
	}
	v := &domain.FlowVersion{ID: uuid.New(), TenantID: w.conv.TenantID, FlowID: flow.ID, Version: 1, Definition: json.RawMessage(s.def), SubflowPins: s.pins}
	w.ver[v.ID] = v
	flow.ActiveVersionID = &v.ID
	flow.Status = domain.FlowStatusPublished
	if s.priority > 0 {
		flow.Priority = s.priority
	}
	if s.policy != "" {
		flow.RestartPolicy = s.policy
	}
	flow.TriggerFilter = s.filter
	if !s.isSub {
		w.runs.candidates = append(w.runs.candidates, flow)
	}
	return v
}

func (w *world) inbound(text string, newConv bool) (Outcome, uuid.UUID, error) {
	id := uuid.New()
	w.runs.msgs[id] = &ports.InboundMessage{ID: id, Text: text, At: w.now}
	out, err := w.eng.OnInbound(w.ctx, InboundEvent{ConversationID: w.conv.ID, MessageID: id, NewConversation: newConv})
	return out, id, err
}

func (w *world) onlyRun() *domain.FlowRun {
	w.t.Helper()
	if len(w.runs.runs) != 1 {
		w.t.Fatalf("expected exactly one run, got %d", len(w.runs.runs))
	}
	for _, r := range w.runs.runs {
		return r
	}
	return nil
}

// failExec is a test-only node type that always errors, to prove the failure path.
type failExec struct{}

func (failExec) Type() domain.NodeType { return domain.NodeType("explode") }
func (failExec) Execute(StepInput) (StepResult, error) {
	return StepResult{}, errors.New("boom")
}

func wf(nodes, edges string, extra string) string {
	return fmt.Sprintf(`{"schema_version":1,"nodes":[%s],"edges":[%s]%s}`, nodes, edges, extra)
}

func edge(id, src, port, dst string) string {
	return fmt.Sprintf(`{"id":%q,"source":%q,"sourcePort":%q,"target":%q}`, id, src, port, dst)
}

var (
	greetFlow = wf(`{"id":"start","type":"trigger"},{"id":"hi","type":"send_message","config":{"text":"Olá {{contact.name}}"}},{"id":"bye","type":"end"}`,
		edge("1", "start", "next", "hi")+","+edge("2", "hi", "next", "bye"), "")
	askFlow = wf(`{"id":"start","type":"trigger"},{"id":"q","type":"ask","config":{"text":"Qual o seu nome?","variable":"nome"}},
	 {"id":"hi","type":"send_message","config":{"text":"Prazer, {{nome}}!"}},{"id":"bye","type":"end"},{"id":"slow","type":"end"}`,
		edge("1", "start", "next", "q")+","+edge("2", "q", "next", "hi")+","+edge("3", "q", "timeout", "slow")+","+edge("4", "hi", "next", "bye"), "")
)

// ---- tests -------------------------------------------------------------------------------------------------------

func TestRunsToCompletionAndNeverStrandsTheConversation(t *testing.T) {
	w := newWorld(t, flowSpec{slug: "greet", def: greetFlow})
	out, _, err := w.inbound("oi", true)
	if err != nil || out != OutcomeStarted {
		t.Fatalf("start: %v %v", out, err)
	}
	r := w.onlyRun()
	if r.Status != domain.RunCompleted || r.NodeExecCount != 3 || r.CompletedAt == nil {
		t.Fatalf("run: %+v", r)
	}
	if len(w.fx.sent) != 1 || w.fx.sent[0] != "Olá Ana" {
		t.Fatalf("sent: %v", w.fx.sent)
	}
	// The bot held the conversation and ended without a handoff: it must go back to the normal queue flow.
	if w.conv.AutomationMode != domain.AutomationNone || len(w.fx.assigned) != 1 || w.fx.assigned[0] != nil {
		t.Fatalf("conversation must return to the default queue: mode=%s assigned=%v", w.conv.AutomationMode, w.fx.assigned)
	}
	if len(w.runs.execs) != 3 || w.runs.execs[1].Seq != 2 || w.runs.execs[1].NodeID != "hi" {
		t.Fatalf("audit trail: %+v", w.runs.execs)
	}
}

func TestAskWaitsThenResumesWithTheAnswer(t *testing.T) {
	w := newWorld(t, flowSpec{slug: "ask", def: askFlow})
	if out, _, err := w.inbound("oi", true); err != nil || out != OutcomeStarted {
		t.Fatalf("start: %v %v", out, err)
	}
	r := w.onlyRun()
	if r.Status != domain.RunWaitingInput || r.CurrentNodeID != "q" || r.WaitUntil == nil || !r.WaitUntil.After(w.now) {
		t.Fatalf("must wait at the question: %+v", r)
	}
	if w.conv.AutomationMode != domain.AutomationBot || len(w.fx.sent) != 1 || w.fx.sent[0] != "Qual o seu nome?" {
		t.Fatalf("bot must hold the conversation and ask: mode=%s sent=%v", w.conv.AutomationMode, w.fx.sent)
	}
	w.now = w.now.Add(30 * time.Second)
	if out, _, err := w.inbound("  Carlos  ", false); err != nil || out != OutcomeResumed {
		t.Fatalf("resume: %v %v", out, err)
	}
	r = w.onlyRun()
	if r.Status != domain.RunCompleted || r.Variables["nome"] != "Carlos" {
		t.Fatalf("answer must be stored trimmed: %+v", r.Variables)
	}
	if got := w.fx.sent[len(w.fx.sent)-1]; got != "Prazer, Carlos!" {
		t.Fatalf("reply: %q", got)
	}
}

func TestDuplicateEventsDoNotDuplicateAnything(t *testing.T) {
	w := newWorld(t, flowSpec{slug: "ask", def: askFlow})
	_, firstID, _ := w.inbound("oi", true)
	sent, execs := len(w.fx.sent), len(w.runs.execs)
	// The same provider event delivered again (JetStream redelivery): same message id.
	out, err := w.eng.OnInbound(w.ctx, InboundEvent{ConversationID: w.conv.ID, MessageID: firstID, NewConversation: true})
	if err != nil || out != OutcomeDuplicate {
		t.Fatalf("start redelivery: %v %v", out, err)
	}
	// The answer delivered twice.
	_, answerID, _ := w.inbound("Carlos", false)
	out, err = w.eng.OnInbound(w.ctx, InboundEvent{ConversationID: w.conv.ID, MessageID: answerID})
	if err != nil || out != OutcomeDuplicate {
		t.Fatalf("answer redelivery: %v %v", out, err)
	}
	if len(w.runs.runs) != 1 || len(w.fx.sent) != sent+1 || len(w.runs.execs) != execs+3 {
		t.Fatalf("duplicates changed state: runs=%d sent=%d execs=%d", len(w.runs.runs), len(w.fx.sent), len(w.runs.execs))
	}
}

func TestInvalidAnswersReaskThenFallToTimeoutPort(t *testing.T) {
	numFlow := wf(`{"id":"start","type":"trigger"},{"id":"q","type":"ask","config":{"text":"Quantos usuários?","variable":"n","validation":"number","max_attempts":2}},{"id":"ok","type":"end"},{"id":"gave_up","type":"end"}`,
		edge("1", "start", "next", "q")+","+edge("2", "q", "next", "ok")+","+edge("3", "q", "timeout", "gave_up"), "")
	w := newWorld(t, flowSpec{slug: "num", def: numFlow})
	w.inbound("oi", true)
	w.inbound("muitos", false) // invalid #1: re-asked
	r := w.onlyRun()
	if r.Status != domain.RunWaitingInput || len(w.fx.sent) != 2 || !strings.Contains(w.fx.sent[1], "Quantos usuários?") {
		t.Fatalf("must re-ask once: status=%s sent=%v", r.Status, w.fx.sent)
	}
	w.inbound("não sei", false) // invalid #2: attempts exhausted -> timeout port
	r = w.onlyRun()
	if r.Status != domain.RunCompleted || w.runs.execs[len(w.runs.execs)-1].NodeID != "gave_up" {
		t.Fatalf("exhausted attempts must take the timeout port: %s last=%s", r.Status, w.runs.execs[len(w.runs.execs)-1].NodeID)
	}
}

func TestTimeoutFiresOnlyWhenDueAndTakesTheTimeoutPort(t *testing.T) {
	w := newWorld(t, flowSpec{slug: "ask", def: askFlow})
	w.inbound("oi", true)
	r := w.onlyRun()
	if out, err := w.eng.OnTimeout(w.ctx, r.ID); err != nil || out != OutcomeIgnored {
		t.Fatalf("not due yet must be ignored: %v %v", out, err)
	}
	w.now = r.WaitUntil.Add(time.Second)
	if out, err := w.eng.OnTimeout(w.ctx, r.ID); err != nil || out != OutcomeTimedOut {
		t.Fatalf("due: %v %v", out, err)
	}
	r = w.onlyRun()
	if r.Status != domain.RunCompleted || w.runs.execs[len(w.runs.execs)-1].NodeID != "slow" {
		t.Fatalf("timeout port: %s %s", r.Status, w.runs.execs[len(w.runs.execs)-1].NodeID)
	}
	if out, _ := w.eng.OnTimeout(w.ctx, r.ID); out != OutcomeIgnored {
		t.Fatal("a finished run must not fire again")
	}
}

func TestChoiceMatchesNumbersAndLabelsAndHandlesOther(t *testing.T) {
	menu := func(withOther bool) string {
		edges := edge("1", "start", "next", "m") + "," + edge("2", "m", "tech", "t") + "," + edge("3", "m", "fin", "f") + "," + edge("4", "m", "timeout", "f")
		if withOther {
			edges += "," + edge("5", "m", "other", "t")
		}
		return wf(`{"id":"start","type":"trigger"},{"id":"m","type":"choice","config":{"text":"Como ajudar?","variable":"topic","options":[{"id":"tech","label":"Suporte","value":"technical"},{"id":"fin","label":"Financeiro"}]}},{"id":"t","type":"end"},{"id":"f","type":"end"}`, edges, "")
	}
	for _, c := range []struct{ reply, wantNode, wantVar string }{{"1", "t", "technical"}, {"financeiro", "f", "Financeiro"}, {"TECH", "t", "technical"}} {
		w := newWorld(t, flowSpec{slug: "menu", def: menu(false)})
		w.inbound("oi", true)
		if got := w.fx.sent[0]; got != "Como ajudar?\n1) Suporte\n2) Financeiro" {
			t.Fatalf("menu text: %q", got)
		}
		w.inbound(c.reply, false)
		r := w.onlyRun()
		if w.runs.execs[len(w.runs.execs)-1].NodeID != c.wantNode || r.Variables["topic"] != c.wantVar {
			t.Errorf("reply %q: node=%s var=%v", c.reply, w.runs.execs[len(w.runs.execs)-1].NodeID, r.Variables["topic"])
		}
	}
	// Free text with an 'other' destination goes there immediately, keeping the text.
	w := newWorld(t, flowSpec{slug: "menu", def: menu(true)})
	w.inbound("oi", true)
	w.inbound("minha vpn caiu", false)
	if r := w.onlyRun(); r.Variables["topic"] != "minha vpn caiu" || w.runs.execs[len(w.runs.execs)-1].NodeID != "t" {
		t.Fatalf("other: %+v", r.Variables)
	}
}

func TestHumanTakeoverSilencesTheBotAndReleasesTheRun(t *testing.T) {
	w := newWorld(t, flowSpec{slug: "ask", def: askFlow})
	w.inbound("oi", true)
	sent := len(w.fx.sent)
	// An operator claims the conversation while the bot waits for the answer.
	op := uuid.New()
	w.conv.AssignedTo = &op
	w.conv.AutomationMode = domain.AutomationWaitingHuman
	out, _, err := w.inbound("Carlos", false)
	if err != nil || out != OutcomeIgnored {
		t.Fatalf("takeover: %v %v", out, err)
	}
	r := w.onlyRun()
	if r.Status != domain.RunCancelled || len(w.fx.sent) != sent {
		t.Fatalf("bot must stay silent and release the run: %s sent=%d/%d", r.Status, len(w.fx.sent), sent)
	}
}

func TestWaitingHumanRunIsNeverAdvancedByTheBot(t *testing.T) {
	w := newWorld(t, flowSpec{slug: "ask", def: askFlow})
	w.inbound("oi", true)
	r := w.onlyRun()
	r.Status = domain.RunWaitingHuman
	out, _, _ := w.inbound("alô?", false)
	if out != OutcomeIgnored || len(w.fx.sent) != 1 {
		t.Fatalf("waiting_human must ignore inbound: %v sent=%d", out, len(w.fx.sent))
	}
}

func TestLimitsAndBrokenGraphsFailCleanly(t *testing.T) {
	// max_node_executions=2 on a 3-node chain
	w := newWorld(t, flowSpec{slug: "greet", def: wf(`{"id":"start","type":"trigger"},{"id":"hi","type":"send_message","config":{"text":"x"}},{"id":"bye","type":"end"}`,
		edge("1", "start", "next", "hi")+","+edge("2", "hi", "next", "bye"), `,"settings":{"max_node_executions":2}`)})
	w.inbound("oi", true)
	if r := w.onlyRun(); r.Status != domain.RunFailed || !strings.Contains(r.Error, "max_node_executions") || w.conv.AutomationMode != domain.AutomationNone || len(w.fx.assigned) != 1 {
		t.Fatalf("limit: %+v mode=%s", r, w.conv.AutomationMode)
	}
	// a port without an edge (a definition that bypassed the validator)
	w = newWorld(t, flowSpec{slug: "broken", def: wf(`{"id":"start","type":"trigger"}`, "", "")})
	w.inbound("oi", true)
	if r := w.onlyRun(); r.Status != domain.RunFailed || !strings.Contains(r.Error, "not connected") {
		t.Fatalf("unrouted port: %+v", r)
	}
	// an executor error
	w = newWorld(t, flowSpec{slug: "boom", def: wf(`{"id":"start","type":"trigger"},{"id":"x","type":"explode"}`, edge("1", "start", "next", "x"), "")})
	w.inbound("oi", true)
	if r := w.onlyRun(); r.Status != domain.RunFailed || !strings.Contains(r.Error, "boom") || w.conv.AutomationMode != domain.AutomationNone {
		t.Fatalf("executor error: %+v", r)
	}
	if len(w.logs) == 0 || !strings.Contains(w.logs[0], "tenant=") || !strings.Contains(w.logs[0], "conversation=") {
		t.Fatalf("failures must be logged with ids and no message text: %v", w.logs)
	}
}

func TestWindowClosedIsRoutableOrFailsCleanly(t *testing.T) {
	routed := wf(`{"id":"start","type":"trigger"},{"id":"hi","type":"send_message","config":{"text":"x"}},{"id":"bye","type":"end"},{"id":"closed","type":"end"}`,
		edge("1", "start", "next", "hi")+","+edge("2", "hi", "next", "bye")+","+edge("3", "hi", "window_closed", "closed"), "")
	w := newWorld(t, flowSpec{slug: "w", def: routed})
	w.fx.status = ports.SendWindowClosed
	w.inbound("oi", true)
	if w.runs.execs[len(w.runs.execs)-1].NodeID != "closed" || w.onlyRun().Status != domain.RunCompleted {
		t.Fatal("a closed window must follow its own port")
	}
	w = newWorld(t, flowSpec{slug: "w2", def: greetFlow})
	w.fx.status = ports.SendWindowClosed
	w.inbound("oi", true)
	if r := w.onlyRun(); r.Status != domain.RunFailed || !strings.Contains(r.Error, "window") {
		t.Fatalf("unrouted closed window must fail the run: %+v", r)
	}
}

func TestStartConditions(t *testing.T) {
	for name, mutate := range map[string]func(*ports.ConversationFacts){
		"internal conversation": func(f *ports.ConversationFacts) { f.Kind = "internal" },
		"spam/other contact":    func(f *ports.ConversationFacts) { f.Kind = "external_other" },
		"already assigned":      func(f *ports.ConversationFacts) { f.AssignedTo = ptr(uuid.New()) },
		"closed":                func(f *ports.ConversationFacts) { f.Status = "closed" },
		"identity conflict":     func(f *ports.ConversationFacts) { f.HasUnclassifiedParticipants = true },
		"no contact":            func(f *ports.ConversationFacts) { f.ContactID = nil },
	} {
		w := newWorld(t, flowSpec{slug: "greet", def: greetFlow})
		mutate(w.conv)
		if out, _, err := w.inbound("oi", true); err != nil || out != OutcomeIgnored || len(w.runs.runs) != 0 || len(w.fx.sent) != 0 {
			t.Errorf("%s: must not start: %v %v runs=%d", name, out, err, len(w.runs.runs))
		}
	}
	// Legacy conversation (mode none, not new) with the default policy is never engaged.
	w := newWorld(t, flowSpec{slug: "greet", def: greetFlow})
	w.conv.AutomationMode = domain.AutomationNone
	if out, _, _ := w.inbound("oi", false); out != OutcomeIgnored || len(w.runs.runs) != 0 {
		t.Fatalf("legacy conversation must be left alone: %v", out)
	}
	// ... but a flow with restart_policy=always may start on it.
	w = newWorld(t, flowSpec{slug: "greet", def: greetFlow, policy: domain.RestartAlways})
	w.conv.AutomationMode = domain.AutomationNone
	if out, _, _ := w.inbound("oi", false); out != OutcomeStarted {
		t.Fatalf("restart_policy=always: %v", out)
	}
}

func TestResolverPrioritySpecificBeforeDefaultAndChannelFilter(t *testing.T) {
	lineA, lineB := uuid.New(), uuid.New()
	mk := func(label string) string {
		return wf(`{"id":"start","type":"trigger"},{"id":"m","type":"send_message","config":{"text":"`+label+`"}},{"id":"e","type":"end"}`, edge("1", "start", "next", "m")+","+edge("2", "m", "next", "e"), "")
	}
	// Candidates arrive already ordered by the repository (specific by priority, defaults last).
	w := newWorld(t,
		flowSpec{slug: "meta-only", def: mk("meta"), filter: domain.TriggerFilter{Providers: []string{"meta_cloud"}}},
		flowSpec{slug: "line-b", def: mk("lineB"), filter: domain.TriggerFilter{ConnectionIDs: []uuid.UUID{lineB}}},
		flowSpec{slug: "default", def: mk("default")})
	w.conv.ConnectionID, w.conv.Provider = &lineA, "waha"
	w.inbound("oi", true)
	if w.fx.sent[0] != "default" {
		t.Fatalf("waha line A matches neither filter: %v", w.fx.sent)
	}
	w2 := newWorld(t,
		flowSpec{slug: "meta-only", def: mk("meta"), filter: domain.TriggerFilter{Providers: []string{"meta_cloud"}}},
		flowSpec{slug: "line-b", def: mk("lineB"), filter: domain.TriggerFilter{ConnectionIDs: []uuid.UUID{lineB}}},
		flowSpec{slug: "default", def: mk("default")})
	w2.conv.ConnectionID, w2.conv.Provider = &lineB, "meta_cloud"
	w2.inbound("oi", true)
	if w2.fx.sent[0] != "meta" {
		t.Fatalf("first matching candidate must win: %v", w2.fx.sent)
	}
	// No flow at all: nothing happens and the legacy routing stays in charge.
	w3 := newWorld(t)
	if out, _, _ := w3.inbound("oi", true); out != OutcomeIgnored || len(w3.runs.runs) != 0 {
		t.Fatalf("no flow: %v", out)
	}
}

func TestSubflowCallPinsVersionAndReturns(t *testing.T) {
	w := newWorld(t)
	sub := w.addFlow(flowSpec{slug: "greeting", isSub: true, def: wf(`{"id":"start","type":"trigger"},{"id":"m","type":"send_message","config":{"text":"from subflow"}},{"id":"e","type":"end"}`,
		edge("1", "start", "next", "m")+","+edge("2", "m", "next", "e"), "")})
	parent := wf(`{"id":"start","type":"trigger"},{"id":"call","type":"subflow","config":{"flow":"greeting"}},{"id":"after","type":"send_message","config":{"text":"back in parent"}},{"id":"bye","type":"end"}`,
		edge("1", "start", "next", "call")+","+edge("2", "call", "next", "after")+","+edge("3", "after", "next", "bye"), "")
	w.addFlow(flowSpec{slug: "main", def: parent, pins: map[string]uuid.UUID{"greeting": sub.ID}})
	w.inbound("oi", true)
	r := w.onlyRun()
	if r.Status != domain.RunCompleted || len(r.CallStack) != 0 {
		t.Fatalf("run: %+v", r)
	}
	if strings.Join(w.fx.sent, "|") != "from subflow|back in parent" {
		t.Fatalf("order: %v", w.fx.sent)
	}
	// Execution log records which version each step ran in.
	vers := map[uuid.UUID]bool{}
	for _, e := range w.runs.execs {
		vers[e.FlowVersionID] = true
	}
	if !vers[sub.ID] || len(vers) != 2 {
		t.Fatalf("steps must be attributed to the pinned versions: %v", vers)
	}
}

func TestSecretsNeverReachTheAuditTrail(t *testing.T) {
	flow := wf(`{"id":"start","type":"trigger"},{"id":"q","type":"ask","config":{"text":"Cole o token","variable":"answer"}},{"id":"e","type":"end"},{"id":"t","type":"end"}`,
		edge("1", "start", "next", "q")+","+edge("2", "q", "next", "e")+","+edge("3", "q", "timeout", "t"), "")
	w := newWorld(t, flowSpec{slug: "s", def: flow})
	w.inbound("oi", true)
	w.inbound("Bearer abcdefghijklmnop1234", false)
	for _, e := range w.runs.execs {
		if strings.Contains(string(e.Input), "abcdefghijklmnop1234") || strings.Contains(string(e.Output), "abcdefghijklmnop1234") {
			t.Fatalf("a secret leaked into the audit trail: %s %s", e.Input, e.Output)
		}
	}
}
