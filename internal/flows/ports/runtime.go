package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/domain"
)

// ConversationFacts is what the engine needs to know about a conversation. LoadConversation returns it under a row lock
// held until the surrounding transaction ends, which is what serializes events of one conversation.
type ConversationFacts struct {
	ID, TenantID   uuid.UUID
	Kind           string // conversations.conversation_kind
	Status         string
	AutomationMode domain.AutomationMode
	AssignedTo     *uuid.UUID
	QueueID        *uuid.UUID
	ConnectionID   *uuid.UUID
	Provider       string
	// IdentityConflict is true while an identity-resolution conflict is open for the contact (ADR-0018): customer automation
	// is suspended for that person until a human decides. An unclassified contact is NOT a conflict.
	IdentityConflict          bool
	ContactID                 *uuid.UUID
	ContactName, ContactPhone string
	ContactKind               string
	ActiveCustomerAccountID   *uuid.UUID
}

type InboundMessage struct {
	ID   uuid.UUID
	Text string
	At   time.Time
}

type CustomerCandidate struct {
	AccountID uuid.UUID `json:"id"`
	Name      string    `json:"name"`
}

type TicketSummary struct {
	Count        int
	FirstID      string
	FirstSubject string
}

type SendStatus string

const (
	SendQueued       SendStatus = "queued"
	SendReplayed     SendStatus = "replayed"
	SendWindowClosed SendStatus = "window_closed"
	SendNoChannel    SendStatus = "no_channel"
)

// RunRepository persists runs and reads the conversation. Every method runs in the engine's tenant system session.
type RunRepository interface {
	LoadConversation(ctx context.Context, id uuid.UUID) (*ConversationFacts, error)
	LoadInboundMessage(ctx context.Context, conversationID, messageID uuid.UUID) (*InboundMessage, error)
	// RunByEvent finds the run that already consumed this inbound message (as its trigger or as a later answer), so a
	// redelivered event is recognised even after its run has ended.
	RunByEvent(ctx context.Context, eventID string) (*domain.FlowRun, error)
	ActiveRun(ctx context.Context, conversationID uuid.UUID) (*domain.FlowRun, error)
	GetRun(ctx context.Context, id uuid.UUID) (*domain.FlowRun, error)
	CandidateFlows(ctx context.Context) ([]*domain.Flow, error)
	CreateRun(ctx context.Context, run *domain.FlowRun) error
	SaveRun(ctx context.Context, run *domain.FlowRun) error
	AppendExecution(ctx context.Context, e *domain.NodeExecution) error
	SetAutomationMode(ctx context.Context, conversationID uuid.UUID, mode domain.AutomationMode) error
}

// VersionReader loads published versions (the FlowRepository satisfies it).
type VersionReader interface {
	GetVersion(ctx context.Context, id uuid.UUID) (*domain.FlowVersion, error)
}

// Effects is everything a node may do to the rest of OMNIRA. Nodes never touch SQL or providers directly: the real
// implementation runs inside the same transaction as the step; the simulator swaps in a recording fake.
type Effects interface {
	CustomerCandidates(ctx context.Context, contactID uuid.UUID) ([]CustomerCandidate, error)
	// ValidateCustomer checks that accountID is an ACTIVE company of this conversation's own contact. It writes nothing: the
	// chosen company is recorded on the run and on the ticket, never on the conversation (ADR-0017/0018).
	ValidateCustomer(ctx context.Context, conversationID, accountID uuid.UUID) error
	OpenTickets(ctx context.Context, f *ConversationFacts) (TicketSummary, error)
	// EnsureTicket makes the conversation's ticket real; customerAccountID (already validated) is stored on the ticket.
	EnsureTicket(ctx context.Context, conversationID uuid.UUID, subject, priority string, customerAccountID *uuid.UUID) (ticketID uuid.UUID, created bool, err error)
	// AssignQueue routes the conversation to queueID, or to the tenant's default queue when nil.
	AssignQueue(ctx context.Context, conversationID uuid.UUID, queueID *uuid.UUID) error
	// Handoff moves the conversation to humans (queue + automation_mode=waiting_human).
	Handoff(ctx context.Context, conversationID uuid.UUID, queueID *uuid.UUID) error
	SendText(ctx context.Context, conversationID uuid.UUID, text, idempotencyKey string) (SendStatus, error)
}

// CustomerCloser is an optional Effects capability: close the attendance because the CONTACT asked for it (the system is
// the actor). It must be idempotent, close the local tickets like an agent's finalize and record source "system".
type CustomerCloser interface {
	CloseByCustomer(ctx context.Context, conversationID uuid.UUID, command string) error
}

// ChoiceOption is one tappable option of a menu sent as buttons/list.
type ChoiceOption struct {
	ID    string
	Title string
}

// ChoiceSender is an optional Effects capability: send a menu as buttons/list where the channel supports it (it falls back
// to the numbered text itself otherwise). text is the numbered text menu, question the sentence above the options.
type ChoiceSender interface {
	SendChoice(ctx context.Context, conversationID uuid.UUID, text, question string, options []ChoiceOption, idempotencyKey string) (SendStatus, error)
}

// AIClassification is a SUGGESTION: the executor decides what to do with it (route or fall back).
type AIClassification struct {
	IntentID   string
	Confidence float64
}

// AIGateway is what the AI nodes use. The real implementation sits on the platform's provider-neutral TextGenerator and the
// AI usage ledger; the simulator supplies scenario-defined answers so a simulation never calls a model.
type AIGateway interface {
	Classify(ctx context.Context, conversationID uuid.UUID, text string, intents []domain.AIIntent) (AIClassification, error)
	Extract(ctx context.Context, conversationID uuid.UUID, text string, fields []domain.AIField) (map[string]string, error)
	Summarize(ctx context.Context, conversationID uuid.UUID, maxMessages int) (string, error)
}

// MessageSource gives the summarizer the contact's recent text messages (tenant session, RLS).
type MessageSource interface {
	RecentInboundTexts(ctx context.Context, conversationID uuid.UUID, limit int) ([]string, error)
}
