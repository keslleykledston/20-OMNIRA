package engine

import (
	"context"
	"time"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	sectorsadapters "github.com/omnira/omnira/internal/sectors/adapters"
)

// RecordFailureForTest exposes the failure bookkeeping of a timer to the integration tests.
func (e *Engine) RecordFailureForTest(tenant, conversation, id uuid.UUID, episode, generation, attempts int, token uuid.UUID, cause error) {
	e.recordFailure(context.Background(), claimed{id: id, tenant: tenant, conversation: conversation, episode: episode, generation: generation, attempts: attempts, token: token, kind: "open_wakeup"}, cause)
}

// RunTimerForTest runs one claimed timer with the given fencing token (the integration tests play both a current and a stale worker).
func (e *Engine) RunTimerForTest(tenant, conversation, id uuid.UUID, episode, generation, attempts int, token uuid.UUID) error {
	return e.runTimer(context.Background(), claimed{id: id, tenant: tenant, conversation: conversation, episode: episode, generation: generation, attempts: attempts, token: token, kind: "open_wakeup"})
}

// TryAssignForTest runs the atomic step for one candidate, as chosen by a (possibly stale) snapshot.
func (e *Engine) TryAssignForTest(tenant, queue, conversation uuid.UUID, cand sectorsadapters.AgentVerdict) (bool, error) {
	var ok bool
	err := platformdb.WithSystemTenantSession(context.Background(), e.pool, tenant, func(c context.Context) error {
		q := platformdb.QuerierFromContext(c, e.pool)
		var now time.Time
		if err := q.QueryRow(c, `SELECT now()`).Scan(&now); err != nil {
			return err
		}
		var err error
		ok, err = tryAssign(c, q, tenant, queue, conversation, cand, "round_robin", now)
		return err
	})
	return ok, err
}
