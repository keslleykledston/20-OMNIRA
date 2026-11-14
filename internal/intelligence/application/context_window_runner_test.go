package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/contextwindow"
	"github.com/omnira/omnira/internal/intelligence/ports"
)

// gateCountingStore counts Sweep/Claim calls so WithLiveGate's "paused means no Sweep/Claim at
// all" contract is checked directly, not inferred from side effects.
type gateCountingStore struct{ sweeps, claims int }

func (s *gateCountingStore) ResolveMessage(context.Context, uuid.UUID) (ports.ResolvedMessage, bool, error) {
	return ports.ResolvedMessage{}, false, nil
}
func (s *gateCountingStore) Touch(context.Context, contextwindow.Policy, uuid.UUID, uuid.UUID, uuid.UUID, time.Time) error {
	return nil
}
func (s *gateCountingStore) Force(context.Context, uuid.UUID, uuid.UUID, contextwindow.Trigger) (bool, error) {
	return false, nil
}
func (s *gateCountingStore) Sweep(context.Context, time.Time, int) (int, error) {
	s.sweeps++
	return 0, nil
}
func (s *gateCountingStore) Claim(context.Context, int, time.Duration) ([]ports.ContextWindow, error) {
	s.claims++
	return nil, nil
}
func (s *gateCountingStore) MessageIDs(context.Context, ports.ContextWindow) ([]uuid.UUID, error) {
	return nil, nil
}
func (s *gateCountingStore) Complete(context.Context, ports.ContextWindow) (bool, error) { return true, nil }
func (s *gateCountingStore) Retry(context.Context, ports.ContextWindow, string, time.Duration) (bool, error) {
	return true, nil
}
func (s *gateCountingStore) Dead(context.Context, ports.ContextWindow, string) (bool, error) {
	return true, nil
}

func noopSession(ctx context.Context, _ uuid.UUID, fn func(context.Context) error) error { return fn(ctx) }

// Paused (gate returns false): ProcessOnce must not sweep or claim anything — the whole point of
// the operator kill switch is that flipping it off stops new work immediately, not just new
// analyses of already-claimed windows.
func TestContextWindowRunnerProcessOnceSkipsEverythingWhenLiveGateIsDisabled(t *testing.T) {
	store := &gateCountingStore{}
	r := NewContextWindowRunner(store, NoopWindowAnalyzer{}, noopSession, DefaultContextWindowRunnerConfig(), nil).
		WithLiveGate(func(context.Context) (bool, error) { return false, nil })

	n, err := r.ProcessOnce(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("ProcessOnce() = (%d, %v), want (0, nil)", n, err)
	}
	if store.sweeps != 0 || store.claims != 0 {
		t.Fatalf("sweeps=%d claims=%d, want 0/0 while paused", store.sweeps, store.claims)
	}
}

// A gate read failure fails safe (same idiom as groupsapp.SLAAlertService.HandleBreach): logged and
// treated as "not enabled", never propagated as an error that would retry-storm the ticker loop.
func TestContextWindowRunnerProcessOnceFailsSafeWhenLiveGateErrors(t *testing.T) {
	store := &gateCountingStore{}
	r := NewContextWindowRunner(store, NoopWindowAnalyzer{}, noopSession, DefaultContextWindowRunnerConfig(), nil).
		WithLiveGate(func(context.Context) (bool, error) { return false, errors.New("boom") })

	n, err := r.ProcessOnce(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("ProcessOnce() = (%d, %v), want (0, nil)", n, err)
	}
	if store.sweeps != 0 || store.claims != 0 {
		t.Fatalf("sweeps=%d claims=%d, want 0/0 on a gate error", store.sweeps, store.claims)
	}
}

// Enabled (gate returns true): ProcessOnce proceeds exactly as a runner without WithLiveGate would.
func TestContextWindowRunnerProcessOnceProceedsWhenLiveGateIsEnabled(t *testing.T) {
	store := &gateCountingStore{}
	r := NewContextWindowRunner(store, NoopWindowAnalyzer{}, noopSession, DefaultContextWindowRunnerConfig(), nil).
		WithLiveGate(func(context.Context) (bool, error) { return true, nil })

	if _, err := r.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	if store.sweeps != 1 || store.claims != 1 {
		t.Fatalf("sweeps=%d claims=%d, want 1/1 while enabled", store.sweeps, store.claims)
	}
}

// No WithLiveGate at all (nil liveGate): behaves exactly as before this change — always enabled.
func TestContextWindowRunnerProcessOnceWithoutALiveGateBehavesAsAlwaysEnabled(t *testing.T) {
	store := &gateCountingStore{}
	r := NewContextWindowRunner(store, NoopWindowAnalyzer{}, noopSession, DefaultContextWindowRunnerConfig(), nil)

	if _, err := r.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	if store.sweeps != 1 || store.claims != 1 {
		t.Fatalf("sweeps=%d claims=%d, want 1/1 without a live gate", store.sweeps, store.claims)
	}
}
