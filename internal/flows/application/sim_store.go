package application

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
)

// simStore is an in-memory RunRepository: the simulator runs the REAL engine against it, so a simulation can never write to
// the database. (It is constructed with no connection of any kind.)
type simStore struct {
	conv  *ports.ConversationFacts
	msgs  map[uuid.UUID]*ports.InboundMessage
	runs  map[uuid.UUID]*domain.FlowRun
	execs []*domain.NodeExecution
	flow  *domain.Flow
}

func (s *simStore) LoadConversation(context.Context, uuid.UUID) (*ports.ConversationFacts, error) {
	return s.conv, nil
}
func (s *simStore) LoadInboundMessage(_ context.Context, _, id uuid.UUID) (*ports.InboundMessage, error) {
	if m, ok := s.msgs[id]; ok {
		return m, nil
	}
	return nil, domain.ErrNotFound
}
func (s *simStore) RunByEvent(_ context.Context, ev string) (*domain.FlowRun, error) {
	for _, r := range s.runs {
		if r.TriggerEventID == ev || r.LastEventID == ev {
			return r, nil
		}
	}
	return nil, domain.ErrNotFound
}
func (s *simStore) ActiveRun(_ context.Context, conv uuid.UUID) (*domain.FlowRun, error) {
	for _, r := range s.runs {
		if r.ConversationID == conv && r.Status.Active() {
			return r, nil
		}
	}
	return nil, domain.ErrNotFound
}
func (s *simStore) GetRun(_ context.Context, id uuid.UUID) (*domain.FlowRun, error) {
	if r, ok := s.runs[id]; ok {
		return r, nil
	}
	return nil, domain.ErrNotFound
}
func (s *simStore) CandidateFlows(context.Context) ([]*domain.Flow, error) {
	return []*domain.Flow{s.flow}, nil
}
func (s *simStore) CreateRun(_ context.Context, r *domain.FlowRun) error {
	s.runs[r.ID] = r
	return nil
}
func (s *simStore) SaveRun(_ context.Context, r *domain.FlowRun) error { s.runs[r.ID] = r; return nil }
func (s *simStore) AppendExecution(_ context.Context, e *domain.NodeExecution) error {
	s.execs = append(s.execs, e)
	return nil
}
func (s *simStore) SetAutomationMode(_ context.Context, _ uuid.UUID, m domain.AutomationMode) error {
	s.conv.AutomationMode = m
	return nil
}

// simVersions serves the simulated version, then any pinned subflow version through the read-only fallback.
type simVersions struct {
	own      map[uuid.UUID]*domain.FlowVersion
	fallback ports.VersionReader
}

func (v simVersions) GetVersion(ctx context.Context, id uuid.UUID) (*domain.FlowVersion, error) {
	if x, ok := v.own[id]; ok {
		return x, nil
	}
	if v.fallback != nil {
		return v.fallback.GetVersion(ctx, id)
	}
	return nil, domain.ErrNoSuchVersion
}

// SimMessage is a message the bot WOULD send; Step is the seq of the node that sent it.
type SimMessage struct {
	Step int    `json:"step"`
	Text string `json:"text"`
}

// SimEffect is a side effect that WOULD happen (ticket, queue, handoff, company).
type SimEffect struct {
	Step   int            `json:"step"` // seq of the node that would do it
	Kind   string         `json:"kind"` // ticket | assign_queue | handoff | company_validated
	Detail map[string]any `json:"detail,omitempty"`
}

// simEffects records instead of acting. Nothing here can reach a database or a provider.
type simEffects struct {
	mu         sync.Mutex
	store      *simStore
	sc         *Scenario
	companies  []ports.CustomerCandidate
	messages   []SimMessage
	effects    []SimEffect
	windowOpen bool
	provider   string
	seen       map[string]bool
}

func stepOfKey(key string) int {
	if i := strings.LastIndex(key, ":"); i >= 0 {
		if n, err := strconv.Atoi(key[i+1:]); err == nil {
			return n
		}
	}
	return 0
}

func (f *simEffects) add(kind string, detail map[string]any) {
	// the engine records a node's execution AFTER it returns, so the node acting now is the next one
	f.effects = append(f.effects, SimEffect{Step: len(f.store.execs) + 1, Kind: kind, Detail: detail})
}

func (f *simEffects) CustomerCandidates(context.Context, uuid.UUID) ([]ports.CustomerCandidate, error) {
	return f.companies, nil
}
func (f *simEffects) ValidateCustomer(_ context.Context, _ uuid.UUID, id uuid.UUID) error {
	for _, c := range f.companies {
		if c.AccountID == id {
			f.add("company_validated", map[string]any{"name": c.Name})
			return nil
		}
	}
	return fmt.Errorf("account %s is not a company of the simulated contact", id)
}
func (f *simEffects) OpenTickets(context.Context, *ports.ConversationFacts) (ports.TicketSummary, error) {
	if f.sc.OpenTickets <= 0 {
		return ports.TicketSummary{}, nil
	}
	return ports.TicketSummary{Count: f.sc.OpenTickets, FirstID: "SIM-TICKET-1", FirstSubject: f.sc.OpenTicketSubject}, nil
}
func (f *simEffects) EnsureTicket(_ context.Context, _ uuid.UUID, subject, priority string, account *uuid.UUID) (uuid.UUID, bool, error) {
	d := map[string]any{"subject": subject, "priority": priority}
	if account != nil {
		d["customer_account_id"] = account.String()
	}
	f.add("ticket", d)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(subject)), true, nil
}
func (f *simEffects) AssignQueue(_ context.Context, _ uuid.UUID, q *uuid.UUID) error {
	d := map[string]any{"queue": "default"}
	if q != nil {
		d["queue"] = q.String()
	}
	f.add("assign_queue", d)
	return nil
}
func (f *simEffects) Handoff(_ context.Context, _ uuid.UUID, q *uuid.UUID) error {
	d := map[string]any{"queue": "current"}
	if q != nil {
		d["queue"] = q.String()
	}
	f.add("handoff", d)
	return nil
}
func (f *simEffects) SendText(_ context.Context, _ uuid.UUID, text, key string) (ports.SendStatus, error) {
	if f.provider == "meta_cloud" && !f.windowOpen {
		return ports.SendWindowClosed, nil
	}
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	if f.seen[key] {
		return ports.SendReplayed, nil
	}
	f.seen[key] = true
	f.messages = append(f.messages, SimMessage{Step: stepOfKey(key), Text: text})
	return ports.SendQueued, nil
}
