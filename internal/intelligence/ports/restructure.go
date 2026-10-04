package ports

import (
	"context"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
)

// RestructureResult reports what a merge or split moved.
type RestructureResult struct {
	Messages     int
	GroupMsgs    int
	Entities     int
	Tickets      int
	NewTopic     *domain.TopicThread // split only
	SourceStatus domain.TopicStatus
}

// RestructureRepository merges and splits topics inside the caller's transaction. Both are human actions that never
// delete history.
type RestructureRepository interface {
	// Merge makes `source` archived and pointing at `target`, after COPYING what it holds (message links, containers,
	// entities, tickets) to the target. Both must be open and in the tenant (domain.ErrInvalidTransition otherwise).
	Merge(ctx context.Context, tenantID, sourceID, targetID uuid.UUID, by *uuid.UUID) (RestructureResult, error)
	// Split creates a new topic from the given messages, MOVING their links out of the source. The source must keep at
	// least one message and every message must currently belong to it.
	Split(ctx context.Context, tenantID, sourceID uuid.UUID, newTopic *domain.TopicThread, messages []MessageRef, by *uuid.UUID) (RestructureResult, error)
	// SourceEntities are the entities that the text of the given messages names (for the split's new topic).
	MessageTexts(ctx context.Context, tenantID uuid.UUID, refs []MessageRef) (map[uuid.UUID]string, error)
}
