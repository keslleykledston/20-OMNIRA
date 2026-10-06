package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
)

// Simulation limits keep a test run cheap and bounded.
const (
	MaxSimEvents    = 30
	MaxSimCompanies = 10
)

type SimContact struct {
	Name  string `json:"name,omitempty"`
	Phone string `json:"phone,omitempty"`
	Kind  string `json:"kind,omitempty"` // unclassified (default) | customer | other
}

type SimCompany struct {
	Name string `json:"name"`
}

// SimEvent is one thing that happens to the conversation: the contact writes, or time passes until the pending wait expires.
type SimEvent struct {
	Type string `json:"type"` // message | timeout
	Text string `json:"text,omitempty"`
}

// Scenario is the input of a simulation. Every field is optional; the defaults model a brand-new, unclassified contact on a
// WAHA line who writes "oi".
type Scenario struct {
	Contact           SimContact   `json:"contact"`
	Provider          string       `json:"provider,omitempty"`    // waha (default) | meta_cloud
	WindowOpen        *bool        `json:"window_open,omitempty"` // Meta 24h window; default true
	Companies         []SimCompany `json:"companies,omitempty"`   // companies the contact is linked to
	OpenTickets       int          `json:"open_tickets,omitempty"`
	OpenTicketSubject string       `json:"open_ticket_subject,omitempty"`
	Now               *time.Time   `json:"now,omitempty"` // the clock (business hours); default: now
	Events            []SimEvent   `json:"events,omitempty"`
}

type SimStep struct {
	Seq      int             `json:"seq"`
	NodeID   string          `json:"node_id"`
	NodeType string          `json:"node_type"`
	Status   string          `json:"status"`
	Port     string          `json:"port,omitempty"`
	Output   json.RawMessage `json:"output,omitempty"`
	Error    string          `json:"error,omitempty"`
}

type SimResult struct {
	// Status: completed | waiting_input | waiting_human | failed | cancelled | blocked (blocking validation errors) | no_run
	Status    string         `json:"status"`
	Steps     []SimStep      `json:"steps"`
	Messages  []SimMessage   `json:"messages"`
	Effects   []SimEffect    `json:"effects"`
	Variables map[string]any `json:"variables"`
	Issues    []domain.Issue `json:"issues,omitempty"`
	Error     string         `json:"error,omitempty"`
	Consumed  int            `json:"events_consumed"`
	Waiting   string         `json:"waiting_at,omitempty"` // node the run is waiting at
}

// Reached reports whether the simulated run executed the node (for expectations).
func (r *SimResult) Reached(nodeID string) bool {
	for _, s := range r.Steps {
		if s.NodeID == nodeID {
			return true
		}
	}
	return false
}

// Simulator runs a definition through the REAL engine with an in-memory store and recording effects. It holds no database
// handle: the only thing it may read (never write) is a pinned subflow version, through the optional fallback reader.
type Simulator struct {
	fallback ports.VersionReader
}

func NewSimulator(fallback ports.VersionReader) *Simulator { return &Simulator{fallback: fallback} }

// SimInput is one simulation. Raw is the definition JSON; Pins fixes subflows (slug -> version id); Versions supplies those
// versions when they are not readable through the fallback (tests, template checks).
type SimInput struct {
	Raw      json.RawMessage
	Pins     map[string]uuid.UUID
	Versions map[uuid.UUID]*domain.FlowVersion
	Scenario Scenario
}

func (sc *Scenario) normalize() error {
	if len(sc.Events) == 0 {
		sc.Events = []SimEvent{{Type: "message", Text: "oi"}}
	}
	if len(sc.Events) > MaxSimEvents || len(sc.Companies) > MaxSimCompanies {
		return fmt.Errorf("%w: a simulation allows at most %d events and %d companies", domain.ErrInvalid, MaxSimEvents, MaxSimCompanies)
	}
	if sc.Events[0].Type != "message" {
		return fmt.Errorf("%w: the first event must be a message (the contact writes first)", domain.ErrInvalid)
	}
	for _, e := range sc.Events {
		if e.Type != "message" && e.Type != "timeout" {
			return fmt.Errorf("%w: unknown event type %q (message or timeout)", domain.ErrInvalid, e.Type)
		}
		if len([]rune(e.Text)) > domain.MaxMessageRunes {
			return fmt.Errorf("%w: a message is limited to %d characters", domain.ErrInvalid, domain.MaxMessageRunes)
		}
	}
	switch sc.Contact.Kind {
	case "", "unclassified", "customer", "other":
	default:
		return fmt.Errorf("%w: contact kind must be unclassified, customer or other", domain.ErrInvalid)
	}
	switch sc.Provider {
	case "", "waha", "meta_cloud":
	default:
		return fmt.Errorf("%w: provider must be waha or meta_cloud", domain.ErrInvalid)
	}
	return nil
}

// Simulate validates the definition (pure rules) and runs the scenario. A definition with blocking errors is not run: the
// issues come back with status "blocked".
func (s *Simulator) Simulate(ctx context.Context, in SimInput) (*SimResult, error) {
	sc := in.Scenario
	if err := sc.normalize(); err != nil {
		return nil, err
	}
	def, err := domain.ParseDefinition(in.Raw)
	if err != nil {
		return &SimResult{Status: "blocked", Issues: []domain.Issue{{Severity: domain.SeverityError, Code: "invalid_definition", Message: err.Error()}}, Steps: []SimStep{}, Messages: []SimMessage{}, Effects: []SimEffect{}}, nil
	}
	if issues := domain.Validate(def, domain.ValidateOptions{}); domain.HasErrors(issues) {
		return &SimResult{Status: "blocked", Issues: issues, Steps: []SimStep{}, Messages: []SimMessage{}, Effects: []SimEffect{}}, nil
	}
	return s.run(ctx, in, sc, def)
}

func (s *Simulator) run(ctx context.Context, in SimInput, sc Scenario, def *domain.Definition) (*SimResult, error) {
	tc, err := systemTenant(ctx)
	if err != nil {
		return nil, err
	}
	clock := time.Now().UTC()
	if sc.Now != nil {
		clock = sc.Now.UTC()
	}
	provider := sc.Provider
	if provider == "" {
		provider = "waha"
	}
	kind, conversationKind := sc.Contact.Kind, "unclassified"
	if kind == "" {
		kind = "unclassified"
	}
	if kind == "customer" {
		conversationKind = "customer_service"
	} else if kind == "other" {
		conversationKind = "external_other"
	}
	name := sc.Contact.Name
	if name == "" {
		name = "Contato Teste"
	}
	contactID, convID, lineID := uuid.New(), uuid.New(), uuid.New()
	facts := &ports.ConversationFacts{ID: convID, TenantID: tc.TenantID, Kind: conversationKind, Status: "open", AutomationMode: domain.AutomationBot,
		ConnectionID: &lineID, Provider: provider, ContactID: &contactID, ContactName: name, ContactPhone: sc.Contact.Phone, ContactKind: kind}

	version := &domain.FlowVersion{ID: uuid.New(), TenantID: tc.TenantID, FlowID: uuid.New(), Version: 1, Definition: in.Raw, SubflowPins: in.Pins}
	flow := &domain.Flow{ID: version.FlowID, TenantID: tc.TenantID, Slug: "simulation", Type: domain.FlowTypeInbound, Status: domain.FlowStatusPublished,
		ActiveVersionID: &version.ID, RestartPolicy: domain.RestartNewConversationOnly}
	store := &simStore{conv: facts, msgs: map[uuid.UUID]*ports.InboundMessage{}, runs: map[uuid.UUID]*domain.FlowRun{}, flow: flow}

	windowOpen := sc.WindowOpen == nil || *sc.WindowOpen
	companies := make([]ports.CustomerCandidate, 0, len(sc.Companies))
	for _, c := range sc.Companies {
		companies = append(companies, ports.CustomerCandidate{AccountID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("sim-company:"+c.Name)), Name: c.Name})
	}
	fx := &simEffects{store: store, sc: &sc, companies: companies, windowOpen: windowOpen, provider: provider}
	own := map[uuid.UUID]*domain.FlowVersion{version.ID: version}
	for id, v := range in.Versions {
		own[id] = v
	}
	eng := NewEngine(store, simVersions{own: own, fallback: s.fallback}, fx, AllExecutors()).
		WithClock(func() time.Time { return clock }).WithLogger(func(string, ...any) {})

	res := &SimResult{}
	for i, ev := range sc.Events {
		if run := activeRun(store); run == nil && i > 0 {
			break // the run ended: remaining events have nothing to act on
		}
		switch ev.Type {
		case "message":
			id := uuid.New()
			store.msgs[id] = &ports.InboundMessage{ID: id, Text: ev.Text, At: clock}
			if _, err := eng.OnInbound(ctx, InboundEvent{ConversationID: convID, MessageID: id, NewConversation: i == 0}); err != nil {
				res.Error = truncate(err.Error(), 300)
			}
			clock = clock.Add(time.Second)
		case "timeout":
			run := activeRun(store)
			if run == nil || run.WaitUntil == nil {
				break
			}
			if run.WaitUntil.After(clock) {
				clock = run.WaitUntil.Add(time.Second)
			}
			if _, err := eng.OnTimeout(ctx, run.ID); err != nil {
				res.Error = truncate(err.Error(), 300)
			}
		}
		res.Consumed = i + 1
		if res.Error != "" {
			break
		}
	}
	return s.collect(store, fx, res), nil
}

func activeRun(s *simStore) *domain.FlowRun {
	for _, r := range s.runs {
		if r.Status.Active() && r.Status != domain.RunWaitingHuman {
			return r
		}
	}
	return nil
}

func (s *Simulator) collect(store *simStore, fx *simEffects, res *SimResult) *SimResult {
	res.Steps = make([]SimStep, 0, len(store.execs))
	for _, e := range store.execs {
		res.Steps = append(res.Steps, SimStep{Seq: e.Seq, NodeID: e.NodeID, NodeType: string(e.NodeType), Status: string(e.Status), Port: e.Port, Output: e.Output, Error: e.Error})
	}
	res.Messages, res.Effects = fx.messages, fx.effects
	if res.Messages == nil {
		res.Messages = []SimMessage{}
	}
	if res.Effects == nil {
		res.Effects = []SimEffect{}
	}
	res.Status = "no_run"
	for _, r := range store.runs {
		res.Status = string(r.Status)
		res.Variables = visibleVariables(r.Variables)
		if r.Error != "" && res.Error == "" {
			res.Error = r.Error
		}
		if r.Status == domain.RunWaitingInput || r.Status == domain.RunWaitingHuman {
			res.Waiting = r.CurrentNodeID
		}
	}
	if res.Variables == nil {
		res.Variables = map[string]any{}
	}
	return res
}

// visibleVariables hides engine-private state ("_state", "_candidates"...) and redacts secrets.
func visibleVariables(vars map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range vars {
		if len(k) > 0 && k[0] == '_' {
			continue
		}
		out[k] = v
	}
	if r, ok := domain.Redact(out).(map[string]any); ok {
		return r
	}
	return out
}

// ErrSimulationNeedsTenant is returned when a simulation is started without a TenantContext.
var ErrSimulationNeedsTenant = errors.New("flows: simulation requires a tenant context")
