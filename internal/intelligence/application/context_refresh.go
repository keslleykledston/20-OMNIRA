package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/contextwindow"
	"github.com/omnira/omnira/internal/intelligence/ports"
)

// ErrContextRefreshThrottled: the caller already asked for this conversation too recently (CONTEXT.2C's "idempotent,
// rate limited" requirement) — never a reason to retry with a shorter interval, just to wait.
var ErrContextRefreshThrottled = errors.New("intelligence: context analysis was already refreshed recently for this conversation")

// ContextRefreshService is the real call-site of ContextWindowStore.Force for the "manual" trigger
// (POST .../context-analysis/refresh, CONTEXT.2C) — it records intent (close the open window now, if any) and
// returns at once; it never waits on the AI analysis itself, which the CONTEXT.2A runner picks up asynchronously.
// The handoff/claim/finalize triggers remain deliberately unwired (CONTEXT-INTELLIGENCE-V2.md): they need their own
// call-sites inside servicectx, a different vertical slice.
type ContextRefreshService struct {
	windows ports.ContextWindowStore
	now     func() time.Time
	// MinEvery bounds how often one conversation may force a window, so a double click or a script cannot turn
	// manual refresh into a cost loop — same shape as CopilotService.allow.
	MinEvery time.Duration

	mu   sync.Mutex
	last map[[2]uuid.UUID]time.Time
}

func NewContextRefreshService(windows ports.ContextWindowStore) *ContextRefreshService {
	return &ContextRefreshService{windows: windows, now: time.Now, MinEvery: 30 * time.Second, last: map[[2]uuid.UUID]time.Time{}}
}

func (s *ContextRefreshService) allow(tenantID, conversationID uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if len(s.last) > 4096 {
		for k, t := range s.last {
			if now.Sub(t) > time.Minute {
				delete(s.last, k)
			}
		}
	}
	key := [2]uuid.UUID{tenantID, conversationID}
	if t, ok := s.last[key]; ok && now.Sub(t) < s.MinEvery {
		return false
	}
	s.last[key] = now
	return true
}

// Refresh forces the conversation's current open window to due now. forced=false (nil error) means there was no
// open window — nothing pending to analyze, not a failure.
func (s *ContextRefreshService) Refresh(ctx context.Context, tenantID, conversationID uuid.UUID) (forced bool, err error) {
	if !s.allow(tenantID, conversationID) {
		return false, ErrContextRefreshThrottled
	}
	return s.windows.Force(ctx, tenantID, conversationID, contextwindow.TriggerManual)
}
