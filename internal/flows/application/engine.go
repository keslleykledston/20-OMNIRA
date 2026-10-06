package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// InboundEvent is a persisted inbound message. The tenant is NOT in it: it comes from the system TenantContext the worker
// derived from trusted state.
type InboundEvent struct {
	ConversationID  uuid.UUID
	MessageID       uuid.UUID
	NewConversation bool
}

type Outcome string

const (
	OutcomeStarted   Outcome = "started"
	OutcomeResumed   Outcome = "resumed"
	OutcomeDuplicate Outcome = "duplicate"
	OutcomeIgnored   Outcome = "ignored"
	OutcomeTimedOut  Outcome = "timed_out"
)

// StepInput is what a node executor receives. Vars is a read-only view (built-ins merged with the run variables).
type StepInput struct {
	Ctx      context.Context
	RunID    uuid.UUID
	Seq      int
	Node     domain.Node
	Vars     map[string]any
	State    map[string]any // this node's private state from a previous visit (attempt counters, options shown)
	Facts    *ports.ConversationFacts
	Message  *ports.InboundMessage // the contact's answer when resuming
	Resuming bool
	TimedOut bool
	Now      time.Time
	Def      *domain.Definition
	Effects  ports.Effects
	Subflows map[string]uuid.UUID
}

// StepResult is the outcome of one node.
type StepResult struct {
	Port     string         // output port to follow; "" when the node waits or ends
	Wait     bool           // suspend until the contact answers (or the timeout port fires)
	WaitSecs int            // how long to wait
	End      bool           // terminal: the run completed
	Handoff  bool           // terminal: waiting for a human
	SetVars  map[string]any // author variables to set
	Reserved map[string]any // engine-owned variables (customer, tickets) to set
	State    map[string]any // this node's private state (nil clears it)
	Output   map[string]any // recorded in the audit trail (redacted)
	Call     *SubflowCall   // enter a subflow
	// ActiveCustomer is the company the contact chose (or the only one they have); the engine records it on the run.
	ActiveCustomer *uuid.UUID
}

// SubflowCall asks the engine to push a frame and continue inside a pinned subflow version.
type SubflowCall struct {
	VersionID uuid.UUID
}

type Executor interface {
	Type() domain.NodeType
	Execute(in StepInput) (StepResult, error)
}

// Engine is the flow runtime. One OnInbound/OnTimeout call is one transactional step of one conversation: the caller
// (the worker) runs it inside platformdb.WithSystemTenantSession, so either everything it did commits or nothing does.
type Engine struct {
	runs     ports.RunRepository
	versions ports.VersionReader
	effects  ports.Effects
	execs    map[domain.NodeType]Executor
	now      func() time.Time
	logf     func(format string, args ...any)
	metrics  Metrics
}

func NewEngine(runs ports.RunRepository, versions ports.VersionReader, effects ports.Effects, execs []Executor) *Engine {
	m := make(map[domain.NodeType]Executor, len(execs))
	for _, e := range execs {
		m[e.Type()] = e
	}
	return &Engine{runs: runs, versions: versions, effects: effects, execs: m, now: func() time.Time { return time.Now().UTC() }, logf: log.Printf, metrics: noMetrics{}}
}

// WithClock is for tests.
func (e *Engine) WithClock(now func() time.Time) *Engine { e.now = now; return e }

// WithMetrics enables runtime counters (worker /metrics).
func (e *Engine) WithMetrics(m Metrics) *Engine {
	if m != nil {
		e.metrics = m
	}
	return e
}

func (e *Engine) WithLogger(f func(string, ...any)) *Engine { e.logf = f; return e }

// Executors returns the registered node types (parity with the catalog is tested).
func (e *Engine) Executors() []domain.NodeType {
	out := make([]domain.NodeType, 0, len(e.execs))
	for t := range e.execs {
		out = append(out, t)
	}
	return out
}

func systemTenant(ctx context.Context) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return nil, errors.New("flows: tenant context required")
	}
	return tc, nil
}

// controls reports whether the bot currently owns the conversation. Authority is derived, never trusted from a flag: the
// moment an operator is assigned, the bot is silent whatever automation_mode says (ADR-0019 §6).
func controls(f *ports.ConversationFacts) bool {
	return f.Status == "open" && f.AutomationMode == domain.AutomationBot && f.AssignedTo == nil
}

// Startable reports whether a conversation may be taken by a flow at all: an open conversation with an external contact
// (customer service or not yet classified: an unknown contact is served, it is not "new customer"), nobody assigned, and no
// open identity conflict. The ingest's gate and the engine use the
// same rule so what is held is exactly what can start.
func Startable(f *ports.ConversationFacts) bool {
	if f.Status != "open" || f.AssignedTo != nil || f.ContactID == nil || f.IdentityConflict {
		return false
	}
	return f.Kind == "customer_service" || f.Kind == "unclassified"
}

// OnInbound processes one inbound message of a conversation.
func (e *Engine) OnInbound(ctx context.Context, ev InboundEvent) (Outcome, error) {
	if _, err := systemTenant(ctx); err != nil {
		return OutcomeIgnored, err
	}
	facts, err := e.runs.LoadConversation(ctx, ev.ConversationID) // row lock: serializes this conversation
	if err != nil {
		return OutcomeIgnored, err
	}
	eventID := ev.MessageID.String()
	if _, err := e.runs.RunByEvent(ctx, eventID); err == nil {
		e.metrics.Run("duplicate")
		return OutcomeDuplicate, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return OutcomeIgnored, err
	}

	run, err := e.runs.ActiveRun(ctx, ev.ConversationID)
	switch {
	case err == nil:
		if run.LastEventID == eventID {
			return OutcomeDuplicate, nil
		}
		if run.Status == domain.RunWaitingHuman {
			return OutcomeIgnored, nil
		}
		if !controls(facts) {
			// A human took the conversation (or it was closed) while the bot was waiting: release it.
			return OutcomeIgnored, e.finish(ctx, facts, run, domain.RunCancelled, "the conversation is no longer controlled by the bot")
		}
		msg, err := e.runs.LoadInboundMessage(ctx, ev.ConversationID, ev.MessageID)
		if err != nil {
			return OutcomeIgnored, err
		}
		return OutcomeResumed, e.resume(ctx, facts, run, msg, false, eventID)
	case !errors.Is(err, domain.ErrNotFound):
		return OutcomeIgnored, err
	}

	if !Startable(facts) {
		return OutcomeIgnored, nil
	}
	flow, err := e.pickFlow(ctx, facts)
	if err != nil || flow == nil {
		return OutcomeIgnored, err
	}
	holdsNew := ev.NewConversation && facts.AutomationMode == domain.AutomationBot
	again := flow.RestartPolicy == domain.RestartAlways && facts.AutomationMode == domain.AutomationNone
	if !holdsNew && !again {
		return OutcomeIgnored, nil
	}
	return OutcomeStarted, e.start(ctx, facts, flow, ev)
}

// OnTimeout fires the timeout port of a run whose wait expired.
func (e *Engine) OnTimeout(ctx context.Context, runID uuid.UUID) (Outcome, error) {
	if _, err := systemTenant(ctx); err != nil {
		return OutcomeIgnored, err
	}
	run, err := e.runs.GetRun(ctx, runID)
	if err != nil {
		return OutcomeIgnored, err
	}
	facts, err := e.runs.LoadConversation(ctx, run.ConversationID)
	if err != nil {
		return OutcomeIgnored, err
	}
	// Re-read under the conversation lock: another event may have resumed the run meanwhile.
	run, err = e.runs.GetRun(ctx, runID)
	if err != nil {
		return OutcomeIgnored, err
	}
	if run.Status != domain.RunWaitingInput || run.WaitUntil == nil || run.WaitUntil.After(e.now()) {
		return OutcomeIgnored, nil
	}
	if !controls(facts) {
		return OutcomeIgnored, e.finish(ctx, facts, run, domain.RunCancelled, "the conversation is no longer controlled by the bot")
	}
	return OutcomeTimedOut, e.resume(ctx, facts, run, nil, true, run.LastEventID)
}

// PickFlow chooses the first candidate (already ordered: specific by priority, defaults last) whose channel filter matches
// the conversation's line.
func PickFlow(flows []*domain.Flow, facts *ports.ConversationFacts) *domain.Flow {
	var conn uuid.UUID
	if facts.ConnectionID != nil {
		conn = *facts.ConnectionID
	}
	for _, f := range flows {
		if f.ActiveVersionID != nil && f.TriggerFilter.Matches(conn, facts.Provider) {
			return f
		}
	}
	return nil
}

func (e *Engine) pickFlow(ctx context.Context, facts *ports.ConversationFacts) (*domain.Flow, error) {
	flows, err := e.runs.CandidateFlows(ctx)
	if err != nil {
		return nil, err
	}
	return PickFlow(flows, facts), nil
}

// ReleaseStranded gives a conversation back to the normal queue flow when the bot holds it but no run exists any more
// (the start failed permanently, the flow vanished...). A bot hold must never outlive its run.
func (e *Engine) ReleaseStranded(ctx context.Context, conversationID uuid.UUID) error {
	if _, err := systemTenant(ctx); err != nil {
		return err
	}
	facts, err := e.runs.LoadConversation(ctx, conversationID) // row lock
	if err != nil {
		return err
	}
	if facts.AutomationMode != domain.AutomationBot {
		return nil
	}
	if _, err := e.runs.ActiveRun(ctx, conversationID); err == nil {
		return nil // a run exists: it owns the hold
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if err := e.runs.SetAutomationMode(ctx, conversationID, domain.AutomationNone); err != nil {
		return err
	}
	if facts.QueueID == nil && facts.AssignedTo == nil {
		return e.effects.AssignQueue(ctx, conversationID, nil)
	}
	return nil
}

func (e *Engine) loadDefinition(ctx context.Context, versionID uuid.UUID) (*domain.FlowVersion, *domain.Definition, error) {
	v, err := e.versions.GetVersion(ctx, versionID)
	if err != nil {
		return nil, nil, err
	}
	def, err := domain.ParseDefinition(v.Definition)
	if err != nil {
		return nil, nil, fmt.Errorf("flows: stored version %s is unreadable: %w", v.ID, err)
	}
	return v, def, nil
}

func (e *Engine) start(ctx context.Context, facts *ports.ConversationFacts, flow *domain.Flow, ev InboundEvent) error {
	version, def, err := e.loadDefinition(ctx, *flow.ActiveVersionID)
	if err != nil {
		return err
	}
	trig := def.Trigger()
	if trig == nil {
		return fmt.Errorf("flows: published version %s has no trigger", version.ID)
	}
	now := e.now()
	eventID := ev.MessageID.String()
	run := &domain.FlowRun{
		ID: uuid.New(), TenantID: facts.TenantID, FlowID: flow.ID, FlowVersionID: version.ID, ConversationID: facts.ID,
		ContactID: facts.ContactID, ActiveCustomerAccountID: facts.ActiveCustomerAccountID, Status: domain.RunRunning,
		CurrentNodeID: trig.ID, Variables: map[string]any{}, TriggerEventID: eventID, LastEventID: eventID, StartedAt: now, UpdatedAt: now,
	}
	if err := e.runs.CreateRun(ctx, run); err != nil {
		return err // ErrDuplicateEvent / ErrConversationBusy: a concurrent twin won; the caller treats it as already handled
	}
	e.metrics.Run("started")
	if facts.AutomationMode != domain.AutomationBot {
		if err := e.runs.SetAutomationMode(ctx, facts.ID, domain.AutomationBot); err != nil {
			return err
		}
		facts.AutomationMode = domain.AutomationBot
	}
	msg, err := e.runs.LoadInboundMessage(ctx, facts.ID, ev.MessageID)
	if err != nil {
		return err
	}
	return e.drive(ctx, facts, run, def, version, msg, false, false)
}

func (e *Engine) resume(ctx context.Context, facts *ports.ConversationFacts, run *domain.FlowRun, msg *ports.InboundMessage, timedOut bool, eventID string) error {
	version, def, err := e.loadDefinition(ctx, e.currentVersion(run))
	if err != nil {
		return e.finish(ctx, facts, run, domain.RunFailed, "the flow version could not be loaded")
	}
	run.LastEventID = eventID
	return e.drive(ctx, facts, run, def, version, msg, true, timedOut)
}

// currentVersion is the version whose definition owns the run's current node: the top of the subflow stack, or the
// version the run started with.
func (e *Engine) currentVersion(run *domain.FlowRun) uuid.UUID {
	if n := len(run.CallStack); n > 0 {
		return run.CallStack[n-1].FlowVersionID
	}
	return run.FlowVersionID
}

func (e *Engine) builtins(facts *ports.ConversationFacts, msg *ports.InboundMessage) map[string]any {
	contact := map[string]any{"name": facts.ContactName, "phone": facts.ContactPhone, "kind": facts.ContactKind,
		"classified": facts.ContactKind != "" && facts.ContactKind != "unclassified"}
	if facts.ContactID != nil {
		contact["id"] = facts.ContactID.String()
	}
	text := ""
	if msg != nil {
		text = msg.Text
	}
	return map[string]any{
		"contact":      contact,
		"conversation": map[string]any{"id": facts.ID.String(), "kind": facts.Kind},
		"message":      map[string]any{"text": text},
		"channel":      map[string]any{"provider": facts.Provider},
	}
}

func view(run *domain.FlowRun, builtin map[string]any) map[string]any {
	v := make(map[string]any, len(run.Variables)+len(builtin))
	for k, val := range run.Variables {
		if k == "_state" {
			continue
		}
		v[k] = val
	}
	for k, val := range builtin {
		v[k] = val
	}
	return v
}

func stateOf(run *domain.FlowRun, nodeID string) map[string]any {
	all, _ := run.Variables["_state"].(map[string]any)
	if all == nil {
		return nil
	}
	s, _ := all[nodeID].(map[string]any)
	return s
}

func setState(run *domain.FlowRun, nodeID string, st map[string]any) {
	all, _ := run.Variables["_state"].(map[string]any)
	if all == nil {
		all = map[string]any{}
	}
	if st == nil {
		delete(all, nodeID)
	} else {
		all[nodeID] = st
	}
	run.Variables["_state"] = all
}

// drive runs nodes until the run waits, hands off, ends or fails. It is the only place that advances a run.
func (e *Engine) drive(ctx context.Context, facts *ports.ConversationFacts, run *domain.FlowRun, def *domain.Definition, version *domain.FlowVersion, msg *ports.InboundMessage, resuming, timedOut bool) error {
	run.Status = domain.RunRunning
	run.WaitUntil = nil
	max := def.Settings.EffectiveMaxExecutions()
	nodeID := run.CurrentNodeID
	for {
		if run.NodeExecCount >= max {
			return e.finish(ctx, facts, run, domain.RunFailed, fmt.Sprintf("max_node_executions (%d) reached", max))
		}
		node := def.NodeByID(nodeID)
		if node == nil {
			return e.finish(ctx, facts, run, domain.RunFailed, fmt.Sprintf("node %q does not exist in the pinned version", nodeID))
		}
		ex, ok := e.execs[node.Type]
		if !ok {
			return e.finish(ctx, facts, run, domain.RunFailed, fmt.Sprintf("no executor for node type %q", node.Type))
		}
		run.NodeExecCount++
		seq := run.NodeExecCount
		started := e.now()
		st := stateOf(run, node.ID)
		in := StepInput{Ctx: ctx, RunID: run.ID, Seq: seq, Node: *node, Vars: view(run, e.builtins(facts, msg)), State: st, Facts: facts,
			Message: msg, Resuming: resuming, TimedOut: timedOut, Now: started, Def: def, Effects: e.effects, Subflows: version.SubflowPins}
		res, err := ex.Execute(in)
		resuming, timedOut = false, false // only the first node after a resume sees the answer
		done := e.now()
		rec := &domain.NodeExecution{ID: uuid.New(), TenantID: run.TenantID, FlowRunID: run.ID, Seq: seq, FlowVersionID: e.currentVersion(run), NodeID: node.ID,
			NodeType: node.Type, Status: domain.ExecCompleted, Port: res.Port, Input: nodeInput(node, in), Output: redactedMap(res.Output),
			StartedAt: started, CompletedAt: done, DurationMs: int(done.Sub(started).Milliseconds())}
		if err != nil {
			rec.Status, rec.Error = domain.ExecFailed, truncate(err.Error(), 500)
			_ = e.runs.AppendExecution(ctx, rec)
			e.metrics.Node(node.Type, rec.Status, done.Sub(started))
			return e.finish(ctx, facts, run, domain.RunFailed, fmt.Sprintf("node %q (%s) failed: %s", node.ID, node.Type, truncate(err.Error(), 300)))
		}
		if res.Wait {
			rec.Status = domain.ExecWaiting
		}
		if err := e.runs.AppendExecution(ctx, rec); err != nil {
			return err
		}
		e.metrics.Node(node.Type, rec.Status, done.Sub(started))
		for k, v := range res.SetVars {
			run.Variables[k] = v
		}
		for k, v := range res.Reserved {
			run.Variables[k] = v
		}
		if res.ActiveCustomer != nil {
			run.ActiveCustomerAccountID, facts.ActiveCustomerAccountID = res.ActiveCustomer, res.ActiveCustomer
		}
		setState(run, node.ID, res.State)

		switch {
		case res.Wait:
			run.Status = domain.RunWaitingInput
			run.CurrentNodeID = node.ID
			until := started.Add(time.Duration(res.WaitSecs) * time.Second)
			run.WaitUntil = &until
			run.UpdatedAt = done
			return e.runs.SaveRun(ctx, run)
		case res.Handoff:
			facts.AutomationMode = domain.AutomationWaitingHuman
			e.metrics.Run("handoff")
			run.Status = domain.RunWaitingHuman
			run.CurrentNodeID = node.ID
			run.UpdatedAt = done
			return e.runs.SaveRun(ctx, run)
		case res.End:
			if n := len(run.CallStack); n > 0 {
				// end of a subflow: pop and continue after the call
				frame := run.CallStack[n-1]
				run.CallStack = run.CallStack[:n-1]
				pv, pdef, perr := e.loadDefinition(ctx, e.currentVersion(run))
				if perr != nil {
					return e.finish(ctx, facts, run, domain.RunFailed, "the caller version could not be loaded")
				}
				def, version = pdef, pv
				next := def.Next(frame.ReturnNodeID, "next")
				if next == "" {
					return e.finish(ctx, facts, run, domain.RunFailed, fmt.Sprintf("subflow call %q has no 'next' destination", frame.ReturnNodeID))
				}
				nodeID = next
				continue
			}
			return e.finish(ctx, facts, run, domain.RunCompleted, "")
		case res.Call != nil:
			if len(run.CallStack) >= domain.MaxSubflowDepth {
				return e.finish(ctx, facts, run, domain.RunFailed, "subflow depth limit reached")
			}
			sv, sdef, serr := e.loadDefinition(ctx, res.Call.VersionID)
			if serr != nil {
				return e.finish(ctx, facts, run, domain.RunFailed, "the subflow version could not be loaded")
			}
			trig := sdef.Trigger()
			if trig == nil {
				return e.finish(ctx, facts, run, domain.RunFailed, "the subflow has no trigger")
			}
			run.CallStack = append(run.CallStack, domain.CallFrame{FlowVersionID: sv.ID, ReturnNodeID: node.ID})
			def, version = sdef, sv
			nodeID = trig.ID
			continue
		}
		next := def.Next(node.ID, res.Port)
		if next == "" {
			return e.finish(ctx, facts, run, domain.RunFailed, fmt.Sprintf("node %q left by port %q, which is not connected", node.ID, res.Port))
		}
		nodeID = next
	}
}

// carryMessage keeps the contact's message visible only to nodes that need it later in the same step.
func (e *Engine) carryMessage(msg *ports.InboundMessage, _ domain.NodeType) *ports.InboundMessage {
	return msg
}

// finish ends the run and never leaves the conversation stranded: whatever the outcome, if the bot held it and nobody
// owns it, it returns to the normal queue flow so a human sees it.
func (e *Engine) finish(ctx context.Context, facts *ports.ConversationFacts, run *domain.FlowRun, status domain.RunStatus, reason string) error {
	now := e.now()
	run.Status, run.Error, run.WaitUntil, run.CompletedAt, run.UpdatedAt = status, reason, nil, &now, now
	if err := e.runs.SaveRun(ctx, run); err != nil {
		return err
	}
	e.metrics.Run(string(status))
	if status != domain.RunCompleted {
		e.logf("flows: run %s tenant=%s flow=%s conversation=%s ended %s: %s", run.ID, run.TenantID, run.FlowID, run.ConversationID, status, reason)
	}
	if facts.AutomationMode == domain.AutomationBot {
		if err := e.runs.SetAutomationMode(ctx, facts.ID, domain.AutomationNone); err != nil {
			return err
		}
		facts.AutomationMode = domain.AutomationNone
		if facts.QueueID == nil && facts.AssignedTo == nil {
			return e.effects.AssignQueue(ctx, facts.ID, nil)
		}
	}
	return nil
}

func nodeInput(n *domain.Node, in StepInput) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"node_type": n.Type, "resuming": in.Resuming, "timed_out": in.TimedOut, "answer": answerText(in.Message)})
	return domain.RedactJSON(b)
}

func answerText(m *ports.InboundMessage) string {
	if m == nil {
		return ""
	}
	return m.Text
}

func redactedMap(m map[string]any) json.RawMessage {
	if m == nil {
		return json.RawMessage(`{}`)
	}
	b, _ := json.Marshal(m)
	return domain.RedactJSON(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
