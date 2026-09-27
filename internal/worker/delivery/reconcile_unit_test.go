package delivery_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/worker/delivery"
)

// fakeReconciliationStore lets Reconciler's own driving logic (ShouldRun,
// Tick's drain-until-short-batch loop, error handling) be tested without any
// real Postgres — the store's own correctness is proven separately in
// reconcile_postgres_test.go against real infrastructure.
type fakeReconciliationStore struct {
	calls   int
	results []int
	err     error
}

func (f *fakeReconciliationStore) ReconcileStrandedQueuedSends(_ context.Context, _, _ time.Duration, _ int) (int, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	if len(f.results) == 0 {
		return 0, nil
	}
	n := f.results[0]
	f.results = f.results[1:]
	return n, nil
}

func TestReconciler_ShouldRun_FalseWhenMaxAgeNotPositive(t *testing.T) {
	r := delivery.NewReconciler(&fakeReconciliationStore{}, 0, 60*time.Second)
	if r.ShouldRun() {
		t.Fatal("ShouldRun() = true, want false for MaxAge=0")
	}
	r2 := delivery.NewReconciler(&fakeReconciliationStore{}, -1*time.Hour, 60*time.Second)
	if r2.ShouldRun() {
		t.Fatal("ShouldRun() = true, want false for negative MaxAge")
	}
}

func TestReconciler_ShouldRun_TrueWhenMaxAgePositive(t *testing.T) {
	r := delivery.NewReconciler(&fakeReconciliationStore{}, 24*time.Hour, 60*time.Second)
	if !r.ShouldRun() {
		t.Fatal("ShouldRun() = false, want true for a positive MaxAge")
	}
}

func TestReconciler_Tick_NoOpWhenDisabled(t *testing.T) {
	store := &fakeReconciliationStore{results: []int{5}}
	r := delivery.NewReconciler(store, 0, 60*time.Second)
	r.Tick(context.Background())
	if store.calls != 0 {
		t.Fatalf("store called %d time(s), want 0 when disabled", store.calls)
	}
}

func TestReconciler_Tick_DrainsUntilShortBatch(t *testing.T) {
	store := &fakeReconciliationStore{results: []int{2, 2, 1}}
	r := delivery.NewReconciler(store, 24*time.Hour, 60*time.Second)
	r.BatchSize = 2
	r.Tick(context.Background())
	if store.calls != 3 {
		t.Fatalf("store called %d time(s), want 3 (drain full batches, stop at the short one)", store.calls)
	}
}

func TestReconciler_Tick_StopsOnError(t *testing.T) {
	store := &fakeReconciliationStore{err: errors.New("boom")}
	r := delivery.NewReconciler(store, 24*time.Hour, 60*time.Second)
	r.Tick(context.Background())
	if store.calls != 1 {
		t.Fatalf("store called %d time(s), want exactly 1 (stop, don't loop, on error)", store.calls)
	}
}
