package db

import (
	"context"
	"log"
	"sync"
	"time"
)

// AfterCommit lets code running inside a tenant session ask for something to run ONLY IF that session's transaction commits: a courtesy that must not outlive a
// failed write (for example a message to a customer about a record that was rolled back). The callbacks run in order, right after the commit, on a context
// detached from the request's cancellation; a panic or a slow callback never changes the outcome already committed.
//
// It returns false when ctx belongs to no tenant session (the caller then does the work now, as it did before). Work registered inside a savepoint that is rolled
// back is dropped with it. Each session has its own queue: a nested system session commits, and runs its callbacks, on its own.
func AfterCommit(ctx context.Context, fn func(ctx context.Context)) bool {
	q, ok := ctx.Value(afterCommitKey{}).(*afterCommitQueue)
	if !ok || fn == nil {
		return false
	}
	q.mu.Lock()
	q.fns = append(q.fns, fn)
	q.mu.Unlock()
	return true
}

type afterCommitKey struct{}

type afterCommitQueue struct {
	mu  sync.Mutex
	fns []func(ctx context.Context)
}

func (q *afterCommitQueue) mark() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.fns)
}

// dropAfter forgets what was registered since mark (a rolled-back savepoint).
func (q *afterCommitQueue) dropAfter(mark int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if mark < len(q.fns) {
		q.fns = q.fns[:mark]
	}
}

const afterCommitTimeout = 20 * time.Second

func (q *afterCommitQueue) run(ctx context.Context) {
	q.mu.Lock()
	fns := q.fns
	q.fns = nil
	q.mu.Unlock()
	if len(fns) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), afterCommitTimeout)
	defer cancel()
	for _, fn := range fns {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("db: an after-commit callback panicked: %v", r)
				}
			}()
			fn(ctx)
		}()
	}
}
