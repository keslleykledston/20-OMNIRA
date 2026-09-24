package adapters

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/tickets/domain"
)

// attemptFixture is the minimal real-Postgres fixture: a tenant, one actor
// user with an active tenant_agent membership, a contact and a conversation
// — the smallest chain the FKs on ticket_external_create_attempts require
// (tenant_id, conversation_id) -> conversations(tenant_id, id).
type attemptFixture struct {
	seed           *pgxpool.Pool
	app            *pgxpool.Pool
	tenantID       uuid.UUID
	actorUserID    uuid.UUID
	conversationID uuid.UUID
}

func requireAttemptStack(t *testing.T) (*attemptFixture, *AttemptStore) {
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

	f := newAttemptFixture(t, seed, app)
	return f, NewAttemptStore(app)
}

var testPhoneCounter int64

// nextTestPhoneSuffix returns a small monotonic counter combined with the
// process start time so concurrently-created fixtures never collide on
// contacts_tenant_phone_uq, without needing digits parsed out of a UUID.
func nextTestPhoneSuffix() int64 {
	return (time.Now().Unix()%100000)*1000 + atomic.AddInt64(&testPhoneCounter, 1)%1000
}

func newAttemptFixture(t *testing.T, seed, app *pgxpool.Pool) *attemptFixture {
	t.Helper()
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	if _, err := seed.Exec(ctx, `INSERT INTO tenants(id, legal_name, status) VALUES ($1, 'PRODUCT.6-K1 test tenant', 'active')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, tenantID) })

	if _, err := seed.Exec(ctx, `INSERT INTO users(id, external_subject, email, display_name, status) VALUES ($1, $2, $3, 'PRODUCT.6-K1 actor', 'active')`,
		userID, "product-6-k1-"+userID.String(), userID.String()+"@example.invalid"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { _, _ = seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })

	var agentRoleID uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE tenant_id IS NULL AND key = 'tenant_agent'`).Scan(&agentRoleID); err != nil {
		t.Fatalf("load tenant_agent role: %v", err)
	}
	if _, err := seed.Exec(ctx, `INSERT INTO memberships(id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,'active')`,
		uuid.New(), tenantID, userID, agentRoleID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	contactID := uuid.New()
	phone := "+1555" + fmt.Sprintf("%09d", nextTestPhoneSuffix())
	if _, err := seed.Exec(ctx, `INSERT INTO contacts(id, tenant_id, display_name, phone_e164) VALUES ($1,$2,'PRODUCT.6-K1 contact',$3)`,
		contactID, tenantID, phone); err != nil {
		t.Fatalf("seed contact: %v", err)
	}

	conversationID := uuid.New()
	if _, err := seed.Exec(ctx, `INSERT INTO conversations(id, tenant_id, contact_id) VALUES ($1,$2,$3)`,
		conversationID, tenantID, contactID); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}

	return &attemptFixture{seed: seed, app: app, tenantID: tenantID, actorUserID: userID, conversationID: conversationID}
}

// withSystemSession runs fn under a full-access (is_system_admin) session
// scoped to the fixture's tenant — sufficient for functional tests that are
// not specifically about the RLS membership boundary (that is covered by
// TestAttemptStoreCrossTenantIsolation below, using a real non-admin
// membership like internal/tenancy/adapters/isolation_test.go).
func (f *attemptFixture) withSystemSession(t *testing.T, fn func(ctx context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return platformdb.WithSystemTenantSession(ctx, f.app, f.tenantID, fn)
}

func TestAttemptStoreAcquireNewKeyIsInFlight(t *testing.T) {
	f, store := requireAttemptStack(t)
	var attempt *domain.ExternalCreateAttempt
	var acquired bool
	err := f.withSystemSession(t, func(ctx context.Context) error {
		var err error
		attempt, acquired, err = store.Acquire(ctx, f.conversationID, f.actorUserID, "gate-A-"+uuid.New().String(), "hash-1")
		return err
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !acquired {
		t.Fatal("expected acquired=true for a new key")
	}
	if attempt.State != domain.AttemptInFlight {
		t.Fatalf("state = %q, want in_flight", attempt.State)
	}
}

func TestAttemptStoreSameKeySameHashReturnsExistingWithoutSecondAcquire(t *testing.T) {
	f, store := requireAttemptStack(t)
	key := "gate-C-" + uuid.New().String()
	var first, second *domain.ExternalCreateAttempt
	var acquiredFirst, acquiredSecond bool
	err := f.withSystemSession(t, func(ctx context.Context) error {
		var err error
		first, acquiredFirst, err = store.Acquire(ctx, f.conversationID, f.actorUserID, key, "hash-1")
		if err != nil {
			return err
		}
		second, acquiredSecond, err = store.Acquire(ctx, f.conversationID, f.actorUserID, key, "hash-1")
		return err
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !acquiredFirst {
		t.Fatal("first Acquire should have acquired=true")
	}
	if acquiredSecond {
		t.Fatal("second Acquire with the same key+hash must NOT report acquired=true — no second provider call is authorized")
	}
	if second.ID != first.ID {
		t.Fatalf("second Acquire returned a different attempt row (%s != %s)", second.ID, first.ID)
	}
}

func TestAttemptStoreSameKeyDifferentHashMismatch(t *testing.T) {
	f, store := requireAttemptStack(t)
	key := "gate-D-" + uuid.New().String()
	err := f.withSystemSession(t, func(ctx context.Context) error {
		if _, _, err := store.Acquire(ctx, f.conversationID, f.actorUserID, key, "hash-1"); err != nil {
			return err
		}
		_, _, err := store.Acquire(ctx, f.conversationID, f.actorUserID, key, "hash-2-different")
		return err
	})
	if !errors.Is(err, ErrAttemptIdempotencyMismatch) {
		t.Fatalf("err = %v, want ErrAttemptIdempotencyMismatch", err)
	}
}

// B/T: two concurrent Acquire calls for the same key must result in exactly
// one acquired=true — proven via the database's UNIQUE(tenant_id,
// idempotency_key) constraint, never a process mutex.
func TestAttemptStoreConcurrentSameKeyAcquiresExactlyOnce(t *testing.T) {
	f, store := requireAttemptStack(t)
	key := "gate-B-" + uuid.New().String()

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	acquiredCount := 0
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := f.withSystemSession(t, func(ctx context.Context) error {
				_, acquired, err := store.Acquire(ctx, f.conversationID, f.actorUserID, key, "hash-concurrent")
				if err != nil {
					return err
				}
				if acquired {
					mu.Lock()
					acquiredCount++
					mu.Unlock()
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
}

func TestAttemptStoreConfirmedSuccessTransitionAndReplay(t *testing.T) {
	f, store := requireAttemptStack(t)
	var attempt *domain.ExternalCreateAttempt
	err := f.withSystemSession(t, func(ctx context.Context) error {
		a, _, err := store.Acquire(ctx, f.conversationID, f.actorUserID, "gate-success-"+uuid.New().String(), "hash-1")
		if err != nil {
			return err
		}
		confirmed, err := store.MarkConfirmedSuccess(ctx, a.ID, "k3g", "28180")
		if err != nil {
			return err
		}
		attempt = confirmed
		return nil
	})
	if err != nil {
		t.Fatalf("MarkConfirmedSuccess: %v", err)
	}
	if attempt.State != domain.AttemptConfirmedSuccess || attempt.ExternalTicketID == nil || *attempt.ExternalTicketID != "28180" {
		t.Fatalf("unexpected attempt after confirm: %+v", attempt)
	}

	// Replay with the SAME identity: idempotent no-op, not an error.
	err = f.withSystemSession(t, func(ctx context.Context) error {
		replayed, err := store.MarkConfirmedSuccess(ctx, attempt.ID, "k3g", "28180")
		if err != nil {
			return err
		}
		if replayed.ExternalTicketID == nil || *replayed.ExternalTicketID != "28180" {
			t.Fatalf("replay must preserve external ticket id, got %+v", replayed)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("replay MarkConfirmedSuccess: %v", err)
	}

	// I: external identity must never be replaced by a second external ID.
	err = f.withSystemSession(t, func(ctx context.Context) error {
		_, err := store.MarkConfirmedSuccess(ctx, attempt.ID, "k3g", "99999-different")
		return err
	})
	if !errors.Is(err, ErrAttemptExternalIdentityMismatch) {
		t.Fatalf("err = %v, want ErrAttemptExternalIdentityMismatch", err)
	}
}

func TestAttemptStoreConfirmedFailureTransition(t *testing.T) {
	f, store := requireAttemptStack(t)
	err := f.withSystemSession(t, func(ctx context.Context) error {
		a, _, err := store.Acquire(ctx, f.conversationID, f.actorUserID, "gate-fail-"+uuid.New().String(), "hash-1")
		if err != nil {
			return err
		}
		failed, err := store.MarkConfirmedFailure(ctx, a.ID)
		if err != nil {
			return err
		}
		if failed.State != domain.AttemptConfirmedFailure {
			t.Fatalf("state = %q, want confirmed_failure", failed.State)
		}
		// H/G: a terminal state must not silently accept an unrelated
		// transition — confirming success on an already-failed row is
		// refused, not overwritten.
		if _, err := store.MarkConfirmedSuccess(ctx, a.ID, "k3g", "1"); !errors.Is(err, ErrAttemptInvalidTransition) {
			t.Fatalf("err = %v, want ErrAttemptInvalidTransition", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// H: OUTCOME_UNKNOWN cannot silently become a new POST-ready state — there
// is no transition back to in_flight, and Acquire on the same key must keep
// returning the existing (non-retryable) attempt, never acquired=true again.
func TestAttemptStoreOutcomeUnknownNeverBecomesRetryable(t *testing.T) {
	f, store := requireAttemptStack(t)
	key := "gate-unknown-" + uuid.New().String()
	err := f.withSystemSession(t, func(ctx context.Context) error {
		a, _, err := store.Acquire(ctx, f.conversationID, f.actorUserID, key, "hash-1")
		if err != nil {
			return err
		}
		unknown, err := store.MarkOutcomeUnknown(ctx, a.ID)
		if err != nil {
			return err
		}
		if unknown.State != domain.AttemptOutcomeUnknown {
			t.Fatalf("state = %q, want outcome_unknown", unknown.State)
		}
		// Same key again: must NOT acquire (no automatic retry path).
		replay, acquired, err := store.Acquire(ctx, f.conversationID, f.actorUserID, key, "hash-1")
		if err != nil {
			return err
		}
		if acquired {
			t.Fatal("Acquire must not report acquired=true for a key already in outcome_unknown")
		}
		if replay.State != domain.AttemptOutcomeUnknown {
			t.Fatalf("state = %q, want outcome_unknown to persist across replay", replay.State)
		}
		// Also refuse an explicit attempt to mark it in_flight-equivalent
		// (confirmed success/failure) without going through a reconciliation
		// path — any transition off outcome_unknown other than what a future
		// reconciliation service does explicitly must be refused here.
		if _, err := store.MarkConfirmedFailure(ctx, a.ID); !errors.Is(err, ErrAttemptInvalidTransition) {
			t.Fatalf("err = %v, want ErrAttemptInvalidTransition", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAttemptStoreProjectionSyncedIsIdempotentAndGuarded(t *testing.T) {
	f, store := requireAttemptStack(t)
	err := f.withSystemSession(t, func(ctx context.Context) error {
		a, _, err := store.Acquire(ctx, f.conversationID, f.actorUserID, "gate-projection-"+uuid.New().String(), "hash-1")
		if err != nil {
			return err
		}
		// Projection completion requires confirmed_success first.
		if _, err := store.MarkProjectionSynced(ctx, a.ID, uuid.New()); !errors.Is(err, ErrAttemptInvalidTransition) {
			t.Fatalf("err = %v, want ErrAttemptInvalidTransition before confirmed_success", err)
		}
		if _, err := store.MarkConfirmedSuccess(ctx, a.ID, "k3g", "28180"); err != nil {
			return err
		}
		localTicketID := uuid.New()
		if _, err := f.seed.Exec(context.Background(),
			`INSERT INTO tickets(id, tenant_id, conversation_id, subject) VALUES ($1,$2,$3,'PRODUCT.6-K1 local ticket')`,
			localTicketID, f.tenantID, f.conversationID); err != nil {
			return err
		}
		synced, err := store.MarkProjectionSynced(ctx, a.ID, localTicketID)
		if err != nil {
			return err
		}
		if synced.ProjectionSyncedAt == nil || synced.LocalTicketID == nil || *synced.LocalTicketID != localTicketID {
			t.Fatalf("unexpected projection sync result: %+v", synced)
		}
		// Idempotent replay: same result, no error, no change.
		again, err := store.MarkProjectionSynced(ctx, a.ID, localTicketID)
		if err != nil {
			t.Fatalf("replay MarkProjectionSynced: %v", err)
		}
		if again.LocalTicketID == nil || *again.LocalTicketID != localTicketID {
			t.Fatalf("replay must preserve local_ticket_id, got %+v", again)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// E: cross-tenant, the same idempotency key is a completely independent
// attempt — no collision, no leakage.
func TestAttemptStoreCrossTenantSameKeyIsIndependent(t *testing.T) {
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
	store := NewAttemptStore(app)
	key := "gate-E-shared-key-" + uuid.New().String()

	var acquiredA, acquiredB bool
	if err := fa.withSystemSession(t, func(ctx context.Context) error {
		_, acquiredA, err = store.Acquire(ctx, fa.conversationID, fa.actorUserID, key, "hash-a")
		return err
	}); err != nil {
		t.Fatalf("tenant A acquire: %v", err)
	}
	if err := fb.withSystemSession(t, func(ctx context.Context) error {
		_, acquiredB, err = store.Acquire(ctx, fb.conversationID, fb.actorUserID, key, "hash-b")
		return err
	}); err != nil {
		t.Fatalf("tenant B acquire: %v", err)
	}
	if !acquiredA || !acquiredB {
		t.Fatalf("both tenants should independently acquire the same literal key: A=%v B=%v", acquiredA, acquiredB)
	}
}

// F: RLS must enforce isolation even for a caller with a real (non-admin)
// membership in a DIFFERENT tenant — mirrors
// internal/tenancy/adapters/isolation_test.go's direct-SQL adversarial
// pattern rather than trusting only the repository's own WHERE clauses.
func TestAttemptStoreCrossTenantIsolationEnforcedByRLS(t *testing.T) {
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
	store := NewAttemptStore(app)

	var victimAttemptID uuid.UUID
	if err := victim.withSystemSession(t, func(ctx context.Context) error {
		a, _, err := store.Acquire(ctx, victim.conversationID, victim.actorUserID, "gate-F-"+uuid.New().String(), "hash-1")
		if err != nil {
			return err
		}
		victimAttemptID = a.ID
		return nil
	}); err != nil {
		t.Fatalf("seed victim attempt: %v", err)
	}

	// attacker has a real active membership only in its own tenant — a
	// direct SELECT for victim's tenant_id must return zero rows, RLS
	// fail-closed, not a repository-level filter.
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
			t.Fatal("attacker session must not be able to read another tenant's attempt row via RLS")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
