package engine

import (
	"context"

	"github.com/google/uuid"
)

// RecordFailureForTest exposes the failure bookkeeping of a timer to the integration tests.
func (e *Engine) RecordFailureForTest(tenant, conversation, id uuid.UUID, episode, generation, attempts int, cause error) {
	e.recordFailure(context.Background(), claimed{id: id, tenant: tenant, conversation: conversation, episode: episode, generation: generation, attempts: attempts, kind: "open_wakeup"}, cause)
}
