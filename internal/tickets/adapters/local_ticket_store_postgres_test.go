package adapters

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PRODUCT.6-K2 proof-completion item 2: prove the real production SQL in
// FindEnrichmentCandidate implements exactly the frozen selection rule
// (status IN open/in_progress/waiting, ORDER BY updated_at DESC, id DESC,
// LIMIT 1) — the same rule already shipped by internal/inbox/adapters.
// PostgresInboundStore.FindOpenByConversation, reused verbatim.
//
// CORRECTION discovered while writing these tests: migration
// 000016_inbound_persistence_invariants.up.sql creates
// tickets_active_conversation_uq — a PARTIAL UNIQUE INDEX on
// tickets(tenant_id, conversation_id) WHERE status IN ('open',
// 'in_progress', 'waiting'). This directly contradicts the PRODUCT.6-K
// audit's claim that "the real schema DOES NOT enforce one active ticket
// per conversation" — that claim was wrong; the earlier audit missed
// migration 000016 (it only inspected 000014/000045/000046). The
// constraint DOES exist and DOES guarantee at most one eligible
// (non-closed/non-resolved) ticket per conversation. Attempting to seed
// two simultaneously-eligible tickets for the same conversation below
// fails with exactly this constraint — proving it, not a test bug. The
// "multiple eligible tickets" and "tie-break by id" cases from the gate's
// test list are therefore structurally UNREACHABLE in the real schema and
// are not testable as originally specified; see
// TestTicketsActiveConversationUniqueConstraintPreventsSecondEligibleTicket
// below for the positive proof of why FindEnrichmentCandidate's ORDER
// BY/LIMIT 1 is defensive-only, never load-bearing for correctness today.
func insertTestTicket(t *testing.T, f *attemptFixture, status string, updatedAt time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := f.seed.Exec(context.Background(),
		`INSERT INTO tickets (id, tenant_id, conversation_id, status, subject, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,'PRODUCT.6-K2 fixture ticket',$5,$5)`,
		id, f.tenantID, f.conversationID, status, updatedAt); err != nil {
		t.Fatalf("insert test ticket: %v", err)
	}
	return id
}

// TestTicketsActiveConversationUniqueConstraintPreventsSecondEligibleTicket
// is the positive proof behind the correction documented above: the real
// schema's tickets_active_conversation_uq partial unique index refuses a
// second open/in_progress/waiting ticket for the same conversation. This is
// why FindEnrichmentCandidate's ORDER BY updated_at DESC, id DESC is
// defensive-only (matches internal/inbox/adapters.
// PostgresInboundStore.FindOpenByConversation's own defensive ordering)
// rather than load-bearing: at most one eligible row can ever exist.
func TestTicketsActiveConversationUniqueConstraintPreventsSecondEligibleTicket(t *testing.T) {
	f, _ := requireAttemptStack(t)
	now := time.Now().UTC()
	insertTestTicket(t, f, "open", now)

	id2 := uuid.New()
	_, err := f.seed.Exec(context.Background(),
		`INSERT INTO tickets (id, tenant_id, conversation_id, status, subject, created_at, updated_at)
		 VALUES ($1,$2,$3,'in_progress','PRODUCT.6-K2 fixture ticket',$4,$4)`,
		id2, f.tenantID, f.conversationID, now)
	if err == nil {
		t.Fatal("expected tickets_active_conversation_uq to reject a second eligible ticket for the same conversation")
	}
}

// FindEnrichmentCandidate still correctly returns THE (singular, guaranteed
// by the DB) eligible ticket when one exists.
func TestLocalTicketStoreFindEnrichmentCandidateReturnsTheEligibleTicket(t *testing.T) {
	f, _ := requireAttemptStack(t)
	store := NewLocalTicketStore(f.app)
	want := insertTestTicket(t, f, "in_progress", time.Now().UTC())

	var got uuid.UUID
	err := f.withSystemSession(t, func(ctx context.Context) error {
		ticket, err := store.FindEnrichmentCandidate(ctx, f.conversationID)
		if err != nil {
			return err
		}
		if ticket == nil {
			t.Fatal("expected a candidate, got nil")
		}
		got = ticket.ID
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("selected %s, want %s", got, want)
	}
}

func TestLocalTicketStoreFindEnrichmentCandidateExcludesClosedEvenIfNewer(t *testing.T) {
	f, _ := requireAttemptStack(t)
	store := NewLocalTicketStore(f.app)
	base := time.Now().UTC().Truncate(time.Second)

	openOlder := insertTestTicket(t, f, "waiting", base.Add(-1*time.Hour))
	insertTestTicket(t, f, "closed", base) // newer, but must be ignored

	var got uuid.UUID
	err := f.withSystemSession(t, func(ctx context.Context) error {
		ticket, err := store.FindEnrichmentCandidate(ctx, f.conversationID)
		if err != nil {
			return err
		}
		if ticket == nil {
			t.Fatal("expected a candidate, got nil")
		}
		got = ticket.ID
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != openOlder {
		t.Fatalf("selected %s, want the older but still-open ticket %s (a newer closed ticket must be ignored)", got, openOlder)
	}
}

func TestLocalTicketStoreFindEnrichmentCandidateReturnsNilWhenNoneEligible(t *testing.T) {
	f, _ := requireAttemptStack(t)
	store := NewLocalTicketStore(f.app)
	insertTestTicket(t, f, "closed", time.Now().UTC())
	insertTestTicket(t, f, "resolved", time.Now().UTC())

	err := f.withSystemSession(t, func(ctx context.Context) error {
		ticket, err := store.FindEnrichmentCandidate(ctx, f.conversationID)
		if err != nil {
			return err
		}
		if ticket != nil {
			t.Fatalf("expected nil (no eligible candidate), got %+v", ticket)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// E: a candidate belonging to another tenant must never be returned, even
// under a real RLS session for a DIFFERENT, legitimate tenant — mirrors the
// PRODUCT.6-K1 adversarial RLS pattern.
func TestLocalTicketStoreFindEnrichmentCandidateEnforcesTenantIsolation(t *testing.T) {
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

	tenantA := newAttemptFixture(t, seed, app)
	tenantB := newAttemptFixture(t, seed, app)
	store := NewLocalTicketStore(app)

	insertTestTicket(t, tenantB, "open", time.Now().UTC())

	err = tenantA.withSystemSession(t, func(ctx context.Context) error {
		ticket, err := store.FindEnrichmentCandidate(ctx, tenantB.conversationID)
		if err != nil {
			return err
		}
		if ticket != nil {
			t.Fatalf("tenant A session must not see tenant B's ticket, got %+v", ticket)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
