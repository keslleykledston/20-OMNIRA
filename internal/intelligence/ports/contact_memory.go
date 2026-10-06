package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ContactMemory is what the copilot and the tool gateway may remember about the contact of a TOPIC (ADR-0020): earlier
// attendances, what is still pending or was promised, and a text search over earlier messages. The contact is derived by
// the implementation from the topic (its primary contact), never given by the caller, so these calls cannot be pointed at
// another contact or tenant. A topic without a primary contact (a group topic) has no memory: ErrReferenceNotFound.
type ContactMemory interface {
	RecentAttendances(ctx context.Context, topicID uuid.UUID, limit int) ([]MemoryAttendance, error)
	OpenFollowUps(ctx context.Context, topicID uuid.UUID, limit int) ([]MemoryFollowUp, error)
	Search(ctx context.Context, topicID uuid.UUID, query string, limit int) ([]MemoryHit, error)
}

// MemoryAttendance is a finalized earlier attendance. Truth is agent_confirmed or ai_inferred.
type MemoryAttendance struct {
	At          time.Time
	Reason      string
	Summary     string
	Truth       string
	TicketsKept int
}

// MemoryFollowUp is something pending, promised or worth remembering, still open.
type MemoryFollowUp struct {
	Kind      string // pending | promise | info
	Text      string
	DueAt     *time.Time
	Truth     string
	CreatedAt time.Time
}

// MemoryHit is an earlier message that matched a search: customer or agent content, a short cleaned snippet.
type MemoryHit struct {
	At      time.Time
	Role    string // customer | agent
	Snippet string
}
