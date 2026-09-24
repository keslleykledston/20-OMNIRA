package adapters

import (
	"context"
	"errors"
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

// PRODUCT.6-O1 sections 9/15 A-C: FindActiveByConversation (the read-path
// name for the exact same primitive FindEnrichmentCandidate calls) returns
// the ticket for each of the three eligible statuses individually.
func TestLocalTicketStoreFindActiveByConversationReturnsEachEligibleStatus(t *testing.T) {
	for _, status := range []string{"open", "in_progress", "waiting"} {
		t.Run(status, func(t *testing.T) {
			f, _ := requireAttemptStack(t)
			store := NewLocalTicketStore(f.app)
			want := insertTestTicket(t, f, status, time.Now().UTC())
			err := f.withSystemSession(t, func(ctx context.Context) error {
				ticket, err := store.FindActiveByConversation(ctx, f.conversationID)
				if err != nil {
					return err
				}
				if ticket == nil || ticket.ID != want {
					t.Fatalf("FindActiveByConversation(%s) = %+v, want id=%s", status, ticket, want)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
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

// ---- PRODUCT.6-O1R real-Postgres proof: EnrichExternalProjection ---------
//
// RefreshTicketProjectionService writes through this exact same method
// (reused verbatim from PRODUCT.6-K2/M4's create/recovery path) — these
// tests prove the SQL-level guarantees a refresh depends on: durable
// external identity cannot be replaced, only provider-owned freshness
// metadata changes, and tenant isolation holds.

func linkTestTicket(t *testing.T, f *attemptFixture, ticketID uuid.UUID, provider, externalID string) {
	t.Helper()
	if _, err := f.seed.Exec(context.Background(),
		`UPDATE tickets SET provider=$2, external_ticket_id=$3, external_status='1', external_status_label='Novo', sync_status='synced', last_synced_at=now()
		 WHERE id=$1`, ticketID, provider, externalID); err != nil {
		t.Fatalf("link test ticket: %v", err)
	}
}

// Provider/external identity cannot be changed by a refresh: a mismatched
// external_ticket_id is refused, and the row is left exactly as it was.
func TestLocalTicketStoreEnrichExternalProjectionRefusesExternalIdentityReplacement(t *testing.T) {
	f, _ := requireAttemptStack(t)
	store := NewLocalTicketStore(f.app)
	ticketID := insertTestTicket(t, f, "open", time.Now().UTC())
	linkTestTicket(t, f, ticketID, "k3g", "28182")

	err := f.withSystemSession(t, func(ctx context.Context) error {
		return store.EnrichExternalProjection(ctx, ticketID, "k3g", "99999", "2", "Em atendimento", time.Now().UTC())
	})
	if !errors.Is(err, ErrLocalTicketExternalIdentityMismatch) {
		t.Fatalf("err = %v, want ErrLocalTicketExternalIdentityMismatch", err)
	}

	// The row must be left exactly as it was before the refused call.
	var externalID, status string
	if scanErr := f.seed.QueryRow(context.Background(),
		`SELECT external_ticket_id, external_status FROM tickets WHERE id=$1`, ticketID).Scan(&externalID, &status); scanErr != nil {
		t.Fatalf("reload ticket: %v", scanErr)
	}
	if externalID != "28182" || status != "1" {
		t.Fatalf("ticket = external_ticket_id=%q external_status=%q, want unchanged 28182/1", externalID, status)
	}
}

// Only the intended provider-owned projection fields change: identity
// (provider, external_ticket_id, conversation_id, local ticket id) and
// every local lifecycle field (status, priority, subject, assigned_to) are
// preserved exactly across a refresh write.
func TestLocalTicketStoreEnrichExternalProjectionUpdatesOnlyFreshnessFields(t *testing.T) {
	f, _ := requireAttemptStack(t)
	store := NewLocalTicketStore(f.app)
	ticketID := insertTestTicket(t, f, "in_progress", time.Now().UTC())
	linkTestTicket(t, f, ticketID, "k3g", "28182")

	syncedAt := time.Now().UTC().Truncate(time.Millisecond)
	err := f.withSystemSession(t, func(ctx context.Context) error {
		return store.EnrichExternalProjection(ctx, ticketID, "k3g", "28182", "2", "Em atendimento", syncedAt)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var status, provider, externalID, extStatus, extStatusLabel, syncStatus, convID string
	var lastSyncedAt time.Time
	if scanErr := f.seed.QueryRow(context.Background(),
		`SELECT status, provider, external_ticket_id, external_status, external_status_label, sync_status, conversation_id::text, last_synced_at
		 FROM tickets WHERE id=$1`, ticketID).Scan(&status, &provider, &externalID, &extStatus, &extStatusLabel, &syncStatus, &convID, &lastSyncedAt); scanErr != nil {
		t.Fatalf("reload ticket: %v", scanErr)
	}
	// Identity and local lifecycle: unchanged.
	if status != "in_progress" || provider != "k3g" || externalID != "28182" || convID != f.conversationID.String() {
		t.Fatalf("identity/lifecycle changed: status=%q provider=%q external_ticket_id=%q conversation_id=%q",
			status, provider, externalID, convID)
	}
	// Provider-owned freshness metadata: updated to the refreshed snapshot.
	if extStatus != "2" || extStatusLabel != "Em atendimento" || syncStatus != "synced" {
		t.Fatalf("freshness metadata = status=%q label=%q sync=%q, want 2/Em atendimento/synced", extStatus, extStatusLabel, syncStatus)
	}
	if !lastSyncedAt.Equal(syncedAt) && lastSyncedAt.Sub(syncedAt).Abs() > time.Second {
		t.Fatalf("last_synced_at = %v, want ~%v", lastSyncedAt, syncedAt)
	}
}

// Tenant isolation: a session scoped to tenant A must not be able to
// refresh/update tenant B's ticket projection at all — the tenant-scoped
// WHERE clause (tenant_id = $1) must exclude the row entirely, surfacing as
// the same "not found" reconciliation path a refresh would hit for any
// other missing-row case, never a cross-tenant write.
func TestLocalTicketStoreEnrichExternalProjectionEnforcesTenantIsolation(t *testing.T) {
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

	ticketB := insertTestTicket(t, tenantB, "open", time.Now().UTC())
	linkTestTicket(t, tenantB, ticketB, "k3g", "28182")

	err = tenantA.withSystemSession(t, func(ctx context.Context) error {
		return store.EnrichExternalProjection(ctx, ticketB, "k3g", "28182", "2", "Em atendimento", time.Now().UTC())
	})
	if err == nil {
		t.Fatal("tenant A session must not be able to enrich tenant B's ticket")
	}

	var extStatus string
	if scanErr := seed.QueryRow(context.Background(), `SELECT external_status FROM tickets WHERE id=$1`, ticketB).Scan(&extStatus); scanErr != nil {
		t.Fatalf("reload tenant B ticket: %v", scanErr)
	}
	if extStatus != "1" {
		t.Fatalf("tenant B ticket external_status = %q, want unchanged 1 (cross-tenant write must never land)", extStatus)
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
