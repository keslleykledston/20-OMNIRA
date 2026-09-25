package adapters

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/tickets/domain"
	"github.com/omnira/omnira/internal/tickets/ports"
)

// PRODUCT.6-O2B1: real-Postgres safety proof for
// ticket_external_status_attempts. Reuses attemptFixture/requireAttemptStack
// (external_create_attempt_postgres_test.go, same package) — no duplicate
// test doubles. f.localTicketID is already a real, seeded (status='closed')
// ticket row, which is all a status-mutation attempt's FK requires.

func newStatusStore(f *attemptFixture) *StatusMutationAttemptStore {
	return NewStatusMutationAttemptStore(f.app)
}

func acquireCmd(f *attemptFixture, key, hash, targetStatus string) ports.AcquireStatusMutationAttemptCommand {
	return ports.AcquireStatusMutationAttemptCommand{
		LocalTicketID: f.localTicketID, ConversationID: f.conversationID, ActorUserID: f.actorUserID,
		IdempotencyKey: key, RequestHash: hash, Provider: "k3g", ExternalTicketID: "28182", TargetStatus: targetStatus,
	}
}

// A. first Acquire: ACQUIRED, state in_flight.
func TestStatusAttemptStoreAcquireNewKeyIsInFlight(t *testing.T) {
	f := requireStatusAttemptStack(t)
	store := newStatusStore(f)
	err := f.withSystemSession(t, func(ctx context.Context) error {
		a, outcome, err := store.Acquire(ctx, acquireCmd(f, "status-a-"+uuid.New().String(), "hash-1", "2"))
		if err != nil {
			return err
		}
		if outcome != ports.AcquireAcquired {
			t.Fatalf("outcome = %q, want acquired", outcome)
		}
		if a.State != domain.AttemptInFlight || a.TargetStatus != "2" || a.Provider != "k3g" || a.ExternalTicketID != "28182" {
			t.Fatalf("unexpected attempt: %+v", a)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// B. same key + same hash: EXISTING_SAME_INTENT, no duplicate row.
func TestStatusAttemptStoreSameKeySameHashReturnsExistingSameIntent(t *testing.T) {
	f := requireStatusAttemptStack(t)
	store := newStatusStore(f)
	key := "status-b-" + uuid.New().String()
	err := f.withSystemSession(t, func(ctx context.Context) error {
		first, _, err := store.Acquire(ctx, acquireCmd(f, key, "hash-1", "2"))
		if err != nil {
			return err
		}
		second, outcome, err := store.Acquire(ctx, acquireCmd(f, key, "hash-1", "2"))
		if err != nil {
			return err
		}
		if outcome != ports.AcquireExistingSameIntent {
			t.Fatalf("outcome = %q, want existing_same_intent", outcome)
		}
		if second.ID != first.ID {
			t.Fatalf("second Acquire returned a different row: %s vs %s", second.ID, first.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var count int
	if err := f.seed.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_external_status_attempts WHERE tenant_id=$1 AND idempotency_key=$2`,
		f.tenantID, key).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want exactly 1 (no duplicate)", count)
	}
}

// C. same key + different hash: IDEMPOTENCY_MISMATCH.
func TestStatusAttemptStoreSameKeyDifferentHashMismatch(t *testing.T) {
	f := requireStatusAttemptStack(t)
	store := newStatusStore(f)
	key := "status-c-" + uuid.New().String()
	err := f.withSystemSession(t, func(ctx context.Context) error {
		if _, _, err := store.Acquire(ctx, acquireCmd(f, key, "hash-1", "2")); err != nil {
			return err
		}
		attempt, outcome, err := store.Acquire(ctx, acquireCmd(f, key, "hash-DIFFERENT", "4"))
		if err != nil {
			t.Fatalf("unexpected Go error (mismatch is a discriminated outcome, not an error): %v", err)
		}
		if outcome != ports.AcquireIdempotencyMismatch {
			t.Fatalf("outcome = %q, want idempotency_mismatch", outcome)
		}
		if attempt != nil {
			t.Fatalf("attempt = %+v, want nil on mismatch", attempt)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// D. different key + in_flight same local ticket: BLOCKED.
func TestStatusAttemptStoreInFlightBlocksDifferentKey(t *testing.T) {
	f := requireStatusAttemptStack(t)
	store := newStatusStore(f)
	err := f.withSystemSession(t, func(ctx context.Context) error {
		if _, _, err := store.Acquire(ctx, acquireCmd(f, "status-d-"+uuid.New().String(), "hash-1", "2")); err != nil {
			return err
		}
		blocking, outcome, err := store.Acquire(ctx, acquireCmd(f, "status-d-other-"+uuid.New().String(), "hash-2", "4"))
		if err != nil {
			return err
		}
		if outcome != ports.AcquireBlockedByUnresolvedOperation {
			t.Fatalf("outcome = %q, want blocked_by_unresolved_operation", outcome)
		}
		if blocking == nil || blocking.State != domain.AttemptInFlight {
			t.Fatalf("blocking attempt = %+v, want in_flight", blocking)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// E. different key + outcome_unknown: BLOCKED.
func TestStatusAttemptStoreOutcomeUnknownBlocksDifferentKey(t *testing.T) {
	f := requireStatusAttemptStack(t)
	store := newStatusStore(f)
	err := f.withSystemSession(t, func(ctx context.Context) error {
		a, _, err := store.Acquire(ctx, acquireCmd(f, "status-e-"+uuid.New().String(), "hash-1", "2"))
		if err != nil {
			return err
		}
		if _, err := store.MarkOutcomeUnknown(ctx, a.ID); err != nil {
			return err
		}
		_, outcome, err := store.Acquire(ctx, acquireCmd(f, "status-e-other-"+uuid.New().String(), "hash-2", "4"))
		if err != nil {
			return err
		}
		if outcome != ports.AcquireBlockedByUnresolvedOperation {
			t.Fatalf("outcome = %q, want blocked_by_unresolved_operation", outcome)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// F. CRITICAL DIFFERENCE FROM CREATE: different key after confirmed_success
// is ALLOWED — a status mutation's success must never permanently block the
// next legitimate lifecycle transition for the same ticket.
func TestStatusAttemptStoreConfirmedSuccessAllowsDifferentKeyToAcquire(t *testing.T) {
	f := requireStatusAttemptStack(t)
	store := newStatusStore(f)
	err := f.withSystemSession(t, func(ctx context.Context) error {
		a, _, err := store.Acquire(ctx, acquireCmd(f, "status-f-"+uuid.New().String(), "hash-1", "2"))
		if err != nil {
			return err
		}
		if _, err := store.MarkConfirmedSuccess(ctx, a.ID, "2", "Em atendimento"); err != nil {
			return err
		}
		next, outcome, err := store.Acquire(ctx, acquireCmd(f, "status-f-next-"+uuid.New().String(), "hash-2", "5"))
		if err != nil {
			return err
		}
		if outcome != ports.AcquireAcquired {
			t.Fatalf("outcome = %q, want acquired (confirmed_success must not block)", outcome)
		}
		if next.TargetStatus != "5" {
			t.Fatalf("next attempt target = %q, want 5", next.TargetStatus)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// G. different key after confirmed_failure is ALLOWED (same as CREATE's
// equivalent rule, for the same reason: a definitive rejection must not
// block a corrected retry).
func TestStatusAttemptStoreConfirmedFailureAllowsDifferentKeyToAcquire(t *testing.T) {
	f := requireStatusAttemptStack(t)
	store := newStatusStore(f)
	err := f.withSystemSession(t, func(ctx context.Context) error {
		a, _, err := store.Acquire(ctx, acquireCmd(f, "status-g-"+uuid.New().String(), "hash-1", "2"))
		if err != nil {
			return err
		}
		if _, err := store.MarkConfirmedFailure(ctx, a.ID); err != nil {
			return err
		}
		_, outcome, err := store.Acquire(ctx, acquireCmd(f, "status-g-retry-"+uuid.New().String(), "hash-2", "2"))
		if err != nil {
			return err
		}
		if outcome != ports.AcquireAcquired {
			t.Fatalf("outcome = %q, want acquired", outcome)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// H. different key for a DIFFERENT local ticket: ALLOWED (the blocking
// index is scoped per local ticket, not per tenant/conversation).
func TestStatusAttemptStoreDifferentLocalTicketAllowsIndependentAcquire(t *testing.T) {
	f := requireStatusAttemptStack(t)
	store := newStatusStore(f)
	otherTicket := insertTestTicket(t, f, "closed", time.Now().UTC())
	err := f.withSystemSession(t, func(ctx context.Context) error {
		if _, _, err := store.Acquire(ctx, acquireCmd(f, "status-h-a-"+uuid.New().String(), "hash-1", "2")); err != nil {
			return err
		}
		cmd := acquireCmd(f, "status-h-b-"+uuid.New().String(), "hash-2", "2")
		cmd.LocalTicketID = otherTicket
		_, outcome, err := store.Acquire(ctx, cmd)
		if err != nil {
			return err
		}
		if outcome != ports.AcquireAcquired {
			t.Fatalf("outcome = %q, want acquired for an unrelated local ticket", outcome)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// I. same idempotency key, different tenants: ALLOWED (tenant-scoped
// uniqueness, mirrors AttemptStore's equivalent).
func TestStatusAttemptStoreCrossTenantSameKeyIsIndependent(t *testing.T) {
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("database URLs required")
	}
	ctx := context.Background()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	fa := newAttemptFixture(t, seed, app)
	fb := newAttemptFixture(t, seed, app)
	store := NewStatusMutationAttemptStore(app)
	key := "status-i-shared-key-" + uuid.New().String()

	var outcomeA, outcomeB ports.AcquireOutcome
	if err := fa.withSystemSession(t, func(ctx context.Context) error {
		_, outcomeA, err = store.Acquire(ctx, acquireCmd(fa, key, "hash-a", "2"))
		return err
	}); err != nil {
		t.Fatalf("tenant A acquire: %v", err)
	}
	if err := fb.withSystemSession(t, func(ctx context.Context) error {
		_, outcomeB, err = store.Acquire(ctx, acquireCmd(fb, key, "hash-b", "2"))
		return err
	}); err != nil {
		t.Fatalf("tenant B acquire: %v", err)
	}
	if outcomeA != ports.AcquireAcquired || outcomeB != ports.AcquireAcquired {
		t.Fatalf("both tenants should independently acquire the same literal key: A=%q B=%q", outcomeA, outcomeB)
	}
}

// J. transition preserves provider/external identity — MarkConfirmedSuccess
// never touches provider/external_ticket_id/target_status, only records the
// reconciled snapshot; replay with the SAME snapshot is a no-op, a
// DIFFERENT one is refused.
func TestStatusAttemptStoreTransitionPreservesProviderAndExternalIdentity(t *testing.T) {
	f := requireStatusAttemptStack(t)
	store := newStatusStore(f)
	var attemptID uuid.UUID
	err := f.withSystemSession(t, func(ctx context.Context) error {
		a, _, err := store.Acquire(ctx, acquireCmd(f, "status-j-"+uuid.New().String(), "hash-1", "5"))
		if err != nil {
			return err
		}
		attemptID = a.ID
		confirmed, err := store.MarkConfirmedSuccess(ctx, a.ID, "5", "Resolvido")
		if err != nil {
			return err
		}
		if confirmed.Provider != "k3g" || confirmed.ExternalTicketID != "28182" || confirmed.TargetStatus != "5" {
			t.Fatalf("identity mutated: %+v", confirmed)
		}
		// Replay with the SAME snapshot: idempotent no-op.
		replayed, err := store.MarkConfirmedSuccess(ctx, a.ID, "5", "Resolvido")
		if err != nil {
			return err
		}
		if replayed.ConfirmedExternalStatus == nil || *replayed.ConfirmedExternalStatus != "5" {
			t.Fatalf("replay must preserve confirmed status, got %+v", replayed)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A DIFFERENT confirmed snapshot on an already confirmed_success row is refused.
	err = f.withSystemSession(t, func(ctx context.Context) error {
		_, err := store.MarkConfirmedSuccess(ctx, attemptID, "6", "Encerrado")
		return err
	})
	if !errors.Is(err, ErrStatusAttemptInvalidTransition) {
		t.Fatalf("err = %v, want ErrStatusAttemptInvalidTransition", err)
	}
}

// K. MarkProjectionSynced only affects the expected attempt, and is
// idempotent.
func TestStatusAttemptStoreMarkProjectionSyncedOnlyAffectsExpectedAttempt(t *testing.T) {
	f := requireStatusAttemptStack(t)
	store := newStatusStore(f)
	otherTicket := insertTestTicket(t, f, "closed", time.Now().UTC())
	err := f.withSystemSession(t, func(ctx context.Context) error {
		a, _, err := store.Acquire(ctx, acquireCmd(f, "status-k-a-"+uuid.New().String(), "hash-1", "2"))
		if err != nil {
			return err
		}
		if _, err := store.MarkConfirmedSuccess(ctx, a.ID, "2", "Em atendimento"); err != nil {
			return err
		}
		otherCmd := acquireCmd(f, "status-k-b-"+uuid.New().String(), "hash-2", "2")
		otherCmd.LocalTicketID = otherTicket
		b, _, err := store.Acquire(ctx, otherCmd)
		if err != nil {
			return err
		}
		synced, err := store.MarkProjectionSynced(ctx, a.ID)
		if err != nil {
			return err
		}
		if synced.ProjectionSyncedAt == nil {
			t.Fatal("expected ProjectionSyncedAt to be set")
		}
		// b (unrelated, still in_flight) must be untouched.
		reloaded, err := store.GetByID(ctx, b.ID)
		if err != nil {
			return err
		}
		if reloaded.ProjectionSyncedAt != nil {
			t.Fatalf("unrelated attempt %s must not be affected, got ProjectionSyncedAt=%v", b.ID, reloaded.ProjectionSyncedAt)
		}
		// Idempotent replay.
		again, err := store.MarkProjectionSynced(ctx, a.ID)
		if err != nil {
			return err
		}
		if !again.ProjectionSyncedAt.Equal(*synced.ProjectionSyncedAt) {
			t.Fatalf("replay changed timestamp: %v vs %v", again.ProjectionSyncedAt, synced.ProjectionSyncedAt)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// L. RLS: a caller with a real (non-admin) membership in a DIFFERENT tenant
// must not be able to read another tenant's status attempt row.
func TestStatusAttemptStoreCrossTenantIsolationEnforcedByRLS(t *testing.T) {
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("database URLs required")
	}
	ctx := context.Background()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	victim := newAttemptFixture(t, seed, app)
	attacker := newAttemptFixture(t, seed, app)
	store := NewStatusMutationAttemptStore(app)

	var victimAttemptID uuid.UUID
	if err := victim.withSystemSession(t, func(ctx context.Context) error {
		a, _, err := store.Acquire(ctx, acquireCmd(victim, "status-l-"+uuid.New().String(), "hash-1", "2"))
		if err != nil {
			return err
		}
		victimAttemptID = a.ID
		return nil
	}); err != nil {
		t.Fatalf("seed victim attempt: %v", err)
	}

	err = platformdb.WithTenantSession(ctx, app, attacker.actorUserID, false, func(scoped context.Context) error {
		tc, err := tenancydomain.NewTenantContext(victim.tenantID, attacker.actorUserID, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		scoped = tenancydomain.WithTenantContext(scoped, tc)
		found, err := store.GetByID(scoped, victimAttemptID)
		if err != nil {
			return err
		}
		if found != nil {
			t.Fatal("attacker session must not be able to read another tenant's status attempt row via RLS")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---- mandatory adversarial concurrency proof (section 23) ----------------

// Same tenant, same local ticket, N concurrent Acquire calls with DIFFERENT
// idempotency keys — the database's partial unique index (migration 000050),
// not a Go-level check, must let exactly one through. Then proves the full
// repeatable lifecycle: confirmed_success releases the barrier for a new
// key; outcome_unknown on that new attempt re-establishes it.
func TestStatusAttemptStoreCrossKeyConcurrentAcquireExactlyOneWinsThenReleases(t *testing.T) {
	f := requireStatusAttemptStack(t)
	store := newStatusStore(f)

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	acquiredCount := 0
	blockedCount := 0
	var winner *domain.ExternalStatusAttempt
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := f.withSystemSession(t, func(ctx context.Context) error {
				key := fmt.Sprintf("status-crosskey-%d-%s", idx, uuid.New().String())
				a, outcome, err := store.Acquire(ctx, acquireCmd(f, key, "hash-crosskey", "2"))
				if err != nil {
					return err
				}
				switch outcome {
				case ports.AcquireBlockedByUnresolvedOperation:
					mu.Lock()
					blockedCount++
					mu.Unlock()
				case ports.AcquireAcquired:
					mu.Lock()
					acquiredCount++
					winner = a
					mu.Unlock()
				default:
					t.Errorf("unexpected outcome %q", outcome)
				}
				return nil
			})
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Acquire failed: %v", err)
		}
	}
	if acquiredCount != 1 {
		t.Fatalf("acquiredCount = %d, want exactly 1", acquiredCount)
	}
	if blockedCount != n-1 {
		t.Fatalf("blockedCount = %d, want %d", blockedCount, n-1)
	}

	var rowCount int
	if err := f.seed.QueryRow(context.Background(), `
		SELECT count(*) FROM ticket_external_status_attempts
		WHERE tenant_id = $1 AND local_ticket_id = $2 AND state IN ('in_flight','outcome_unknown')`,
		f.tenantID, f.localTicketID).Scan(&rowCount); err != nil {
		t.Fatalf("count unresolved rows: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("unresolved attempt rows for local ticket = %d, want exactly 1", rowCount)
	}

	// Transition the winner to confirmed_success — this MUST release the
	// barrier for a brand-new key (critical difference from CREATE).
	err := f.withSystemSession(t, func(ctx context.Context) error {
		if _, err := store.MarkConfirmedSuccess(ctx, winner.ID, "2", "Em atendimento"); err != nil {
			return err
		}
		next, outcome, err := store.Acquire(ctx, acquireCmd(f, "status-crosskey-next-"+uuid.New().String(), "hash-next", "4"))
		if err != nil {
			return err
		}
		if outcome != ports.AcquireAcquired {
			t.Fatalf("outcome = %q, want acquired after confirmed_success", outcome)
		}
		// Now mark THIS new attempt outcome_unknown — the barrier must be
		// re-established (still blocking, unlike confirmed_success/failure).
		if _, err := store.MarkOutcomeUnknown(ctx, next.ID); err != nil {
			return err
		}
		_, outcome2, err := store.Acquire(ctx, acquireCmd(f, "status-crosskey-blocked-"+uuid.New().String(), "hash-blocked", "5"))
		if err != nil {
			return err
		}
		if outcome2 != ports.AcquireBlockedByUnresolvedOperation {
			t.Fatalf("outcome = %q, want blocked_by_unresolved_operation after outcome_unknown", outcome2)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// requireStatusAttemptStack mirrors requireAttemptStack's connection setup
// (external_create_attempt_postgres_test.go) — same fixture, different
// store under test.
func requireStatusAttemptStack(t *testing.T) *attemptFixture {
	t.Helper()
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("database URLs required")
	}
	ctx := context.Background()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(seed.Close)
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return newAttemptFixture(t, seed, app)
}
