package flowstest

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/ports"
)

// SeedConversation creates an unclassified-contact conversation (the state of every new contact, ADR-0018) held by the
// given automation mode for tenant. A 'customer' contact would need an account link, which tests add explicitly.
func (e *Env) SeedConversation(t *testing.T, tenant uuid.UUID, mode string) (conversationID, contactID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	contactID, conversationID = uuid.New(), uuid.New()
	phone := "+5511" + contactID.String()[:8] // only digits are valid in E.164; derive deterministic digits below
	digits := ""
	for _, r := range contactID.String() {
		if r >= '0' && r <= '9' {
			digits += string(r)
		}
	}
	phone = "+55119" + (digits + "00000000")[:8]
	if _, err := e.Seed.Exec(ctx, `INSERT INTO contacts(id, tenant_id, display_name, phone_e164) VALUES($1,$2,'Ana',$3)`, contactID, tenant, phone); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Seed.Exec(ctx, `INSERT INTO conversations(id, tenant_id, contact_id, conversation_kind, automation_mode) VALUES($1,$2,$3,'unclassified',$4)`, conversationID, tenant, contactID, mode); err != nil {
		t.Fatal(err)
	}
	return conversationID, contactID
}

// SeedInbound stores an inbound message and returns its id.
func (e *Env) SeedInbound(t *testing.T, tenant, conversation uuid.UUID, text string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := e.Seed.Exec(context.Background(), `INSERT INTO messages(id, tenant_id, conversation_id, direction, body, status) VALUES($1,$2,$3,'inbound',$4,'received')`, id, tenant, conversation, text); err != nil {
		t.Fatal(err)
	}
	return id
}

// RecordingEffects implements ports.Effects with no side effects outside memory. Sends are idempotent by key, exactly like
// the real system sender, so tests can assert "exactly once" under concurrency.
type RecordingEffects struct {
	mu       sync.Mutex
	Sent     []string
	keys     map[string]bool
	Assigned []*uuid.UUID
	Handoffs int
}

func (f *RecordingEffects) SentTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Sent...)
}

func (f *RecordingEffects) CustomerCandidates(context.Context, uuid.UUID) ([]ports.CustomerCandidate, error) {
	return nil, nil
}
func (f *RecordingEffects) SetActiveCustomer(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (f *RecordingEffects) OpenTickets(context.Context, *ports.ConversationFacts) (ports.TicketSummary, error) {
	return ports.TicketSummary{}, nil
}
func (f *RecordingEffects) EnsureTicket(context.Context, uuid.UUID, string, string) (uuid.UUID, bool, error) {
	return uuid.New(), true, nil
}
func (f *RecordingEffects) AssignQueue(_ context.Context, _ uuid.UUID, q *uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Assigned = append(f.Assigned, q)
	return nil
}
func (f *RecordingEffects) Handoff(context.Context, uuid.UUID, *uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Handoffs++
	return nil
}
func (f *RecordingEffects) SendText(_ context.Context, _ uuid.UUID, text, key string) (ports.SendStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keys == nil {
		f.keys = map[string]bool{}
	}
	if f.keys[key] {
		return ports.SendReplayed, nil
	}
	f.keys[key] = true
	f.Sent = append(f.Sent, text)
	return ports.SendQueued, nil
}
