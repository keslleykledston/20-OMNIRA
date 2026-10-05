package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Status string
type Priority string

const (
	StatusOpen       Status   = "open"
	StatusInProgress Status   = "in_progress"
	StatusWaiting    Status   = "waiting"
	StatusResolved   Status   = "resolved"
	StatusClosed     Status   = "closed"
	PriorityCritical Priority = "critical"
	PriorityHigh     Priority = "high"
	PriorityMedium   Priority = "medium"
	PriorityLow      Priority = "low"
)

type Ticket struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	ConversationID uuid.UUID
	Status         Status
	Priority       Priority
	Subject        string
	AssignedTo     *uuid.UUID
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ResolvedAt     *time.Time
	ClosedAt       *time.Time
	// TopicScoped marks a ticket opened for one subject of a conversation that already had its conversation ticket
	// (ADR-0017). Such tickets are outside the at-most-one-active-per-conversation guarantee.
	TopicScoped bool

	// PRODUCT.6-D (ADR-0013): external ERP ticket projection/link, all
	// nullable. A ticket created locally (still the only path today — see
	// internal/inbox/application/inbound.go) has every one of these fields
	// nil: it is not yet — and may never become — backed by a real tenant
	// ERP connector. No provider-specific type here; the domain must stay
	// implementable against any future provider, not just K3G.
	Provider            *string
	ExternalTicketID    *string
	ExternalStatus      *string
	ExternalStatusLabel *string
	SyncStatus          *string
	LastSyncedAt        *time.Time
}

func NewTicket(tenantID, conversationID uuid.UUID, subject string) (*Ticket, error) {
	if tenantID == uuid.Nil || conversationID == uuid.Nil {
		return nil, errors.New("ticket: tenant and conversation are required")
	}
	now := time.Now().UTC()
	return &Ticket{ID: uuid.New(), TenantID: tenantID, ConversationID: conversationID, Status: StatusOpen, Priority: PriorityMedium, Subject: subject, CreatedAt: now, UpdatedAt: now}, nil
}

func (t *Ticket) Resolve() {
	now := time.Now().UTC()
	t.Status, t.ResolvedAt, t.UpdatedAt = StatusResolved, &now, now
}

func (t *Ticket) Close() {
	now := time.Now().UTC()
	t.Status, t.ClosedAt, t.UpdatedAt = StatusClosed, &now, now
}

// The inbound flow creates an empty-subject local ticket for every conversation
// (internal/inbox/application/inbound.go) so the ERP flow has something to
// enrich. That placeholder is plumbing, not workload: nothing ever resolves it.
// A ticket is "real" once it is linked to the ERP or someone gave it a subject.
// Read models that present tickets to operators (Dashboard, Tickets, Contact
// 360) count only real tickets; the ERP and reconciliation flows keep seeing
// every row.

// IsPlaceholder reports whether the ticket is still the implicit one. It cannot see topic links (a read-model concern):
// use RealTicketSQL for counts and lists.
func (t *Ticket) IsPlaceholder() bool {
	return t.ExternalTicketID == nil && strings.TrimSpace(t.Subject) == ""
}

// RealTicketSQL is the SQL form of "not a placeholder" for read models. Pass the
// table alias ("t") or "" when the query selects FROM tickets without an alias.
// A ticket is real when it is linked to the ERP, has a subject, or a person (or the
// policy) made it the PRIMARY ticket of a topic (ADR-0017): adopting the conversation
// ticket for a subject is a decision, so it stops being the implicit one.
func RealTicketSQL(alias string) string {
	prefix := alias
	if prefix == "" {
		prefix = "tickets"
	}
	prefix += "."
	return "(" + prefix + "external_ticket_id IS NOT NULL OR btrim(" + prefix + "subject) <> '' OR EXISTS (" +
		"SELECT 1 FROM topic_ticket_links ttl WHERE ttl.tenant_id = " + prefix + "tenant_id AND ttl.ticket_id = " + prefix + "id AND ttl.relation = 'primary'))"
}
