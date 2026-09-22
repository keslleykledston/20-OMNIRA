package adapters

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type fakePresenceChecker struct {
	mu            sync.Mutex
	online        map[uuid.UUID]bool
	err           error
	calls         int
	lastBatchSize int
}

func (f *fakePresenceChecker) OnlineMembers(_ context.Context, _ uuid.UUID, agentProfileIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastBatchSize = len(agentProfileIDs)
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[uuid.UUID]bool, len(agentProfileIDs))
	for _, id := range agentProfileIDs {
		out[id] = f.online[id]
	}
	return out, nil
}

func (f *fakePresenceChecker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// panicPresenceChecker fails the test if it is ever called — used to prove
// zero Valkey coupling when routing_require_presence is false.
type panicPresenceChecker struct{ t *testing.T }

func (p panicPresenceChecker) OnlineMembers(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error) {
	p.t.Fatal("presence checker must not be called when routing_require_presence is false")
	return nil, nil
}

// presenceRoutingFixture seeds a tenant, a round-robin queue, a contact, and
// an unassigned conversation in that queue, ready for candidates to be added.
type presenceRoutingFixture struct {
	seed, app      *pgxpool.Pool
	tenantID       uuid.UUID
	queueID        uuid.UUID
	contactID      uuid.UUID
	conversationID uuid.UUID
	roleID         uuid.UUID
}

func newPresenceRoutingFixture(t *testing.T, requirePresence bool) *presenceRoutingFixture {
	t.Helper()
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("database URLs required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		seed.Close()
		t.Fatal(err)
	}
	f := &presenceRoutingFixture{
		seed: seed, app: app,
		tenantID:       uuid.New(),
		queueID:        uuid.New(),
		contactID:      uuid.New(),
		conversationID: uuid.New(),
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL LIMIT 1`).Scan(&f.roleID); err != nil {
		t.Fatal(err)
	}
	_, err = seed.Exec(ctx, `INSERT INTO tenants(id,legal_name,status,routing_require_presence) VALUES($1,$2,'active',$3)`, f.tenantID, "B1 presence routing", requirePresence)
	must(err)
	_, err = seed.Exec(ctx, `INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'Contato','+5511999990001')`, f.contactID, f.tenantID)
	must(err)
	_, err = seed.Exec(ctx, `INSERT INTO queues(id,tenant_id,name,mode) VALUES($1,$2,'RR','round_robin')`, f.queueID, f.tenantID)
	must(err)
	_, err = seed.Exec(ctx, `INSERT INTO conversations(id,tenant_id,contact_id,queue_id,status) VALUES($1,$2,$3,$4,'open')`, f.conversationID, f.tenantID, f.contactID, f.queueID)
	must(err)
	t.Cleanup(func() {
		_, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, f.tenantID)
		seed.Close()
		app.Close()
	})
	return f
}

// addAgent seeds one eligible queue member (active membership, active
// AgentProfile, active+available queue_members, capacity 1) and returns its
// user_id and agent_profile_id.
func (f *presenceRoutingFixture) addAgent(t *testing.T, lastAssignedAt *time.Time) (userID, agentProfileID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	userID = uuid.New()
	if _, err := f.seed.Exec(ctx, `INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, userID, userID, userID.String()+"@invalid"); err != nil {
		t.Fatal(err)
	}
	var membershipID uuid.UUID
	if err := f.seed.QueryRow(ctx, `INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active') RETURNING id`, f.tenantID, userID, f.roleID).Scan(&membershipID); err != nil {
		t.Fatal(err)
	}
	if err := f.seed.QueryRow(ctx, `INSERT INTO agent_profiles(tenant_id,membership_id,status) VALUES($1,$2,'active') RETURNING id`, f.tenantID, membershipID).Scan(&agentProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.seed.Exec(ctx, `INSERT INTO queue_members(tenant_id,queue_id,user_id,active,available,capacity,last_assigned_at) VALUES($1,$2,$3,true,true,1,$4)`,
		f.tenantID, f.queueID, userID, lastAssignedAt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	return userID, agentProfileID
}

// assignRoundRobin wraps the call in the same system-actor DB session
// (SET LOCAL app.is_system_admin=true) the real worker path establishes
// before calling AssignRoundRobin (internal/worker/routing/postgres.go) —
// without it, RLS on tenants/conversations/etc. hides everything from the
// omnira_app role and every query looks like "no rows".
func (f *presenceRoutingFixture) assignRoundRobin(t *testing.T, repo *PostgresAssignmentRepository, reason string) (uuid.UUID, bool, error) {
	t.Helper()
	var assigned uuid.UUID
	var ok bool
	err := platformdb.WithTenantSession(context.Background(), f.app, uuid.Nil, true, func(sctx context.Context) error {
		tc, tcErr := tenancydomain.NewTenantContext(f.tenantID, uuid.Nil, tenancydomain.AccessSourceSystem)
		if tcErr != nil {
			return tcErr
		}
		ctx := tenancydomain.WithTenantContext(sctx, tc)
		var innerErr error
		assigned, ok, innerErr = repo.AssignRoundRobin(ctx, f.conversationID, reason)
		return innerErr
	})
	return assigned, ok, err
}

func (f *presenceRoutingFixture) assignedUser(t *testing.T) *uuid.UUID {
	t.Helper()
	var v *uuid.UUID
	if err := f.seed.QueryRow(context.Background(), `SELECT assigned_to_user_id FROM conversations WHERE id=$1`, f.conversationID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func at(seconds int) *time.Time {
	t := time.Now().Add(time.Duration(seconds) * time.Second)
	return &t
}

func TestAssignRoundRobin_FlagOff_NeverCallsPresence(t *testing.T) {
	f := newPresenceRoutingFixture(t, false)
	userID, _ := f.addAgent(t, nil)
	repo := NewPostgresAssignmentRepository(f.app, panicPresenceChecker{t: t})

	assigned, ok, err := f.assignRoundRobin(t, repo, "round_robin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok || assigned != userID {
		t.Fatalf("expected legacy behavior to assign the only candidate, got ok=%v assigned=%v", ok, assigned)
	}
}

func TestAssignRoundRobin_FlagOn_OnlineCandidate_IsAssigned(t *testing.T) {
	f := newPresenceRoutingFixture(t, true)
	userID, agentProfileID := f.addAgent(t, nil)
	checker := &fakePresenceChecker{online: map[uuid.UUID]bool{agentProfileID: true}}
	repo := NewPostgresAssignmentRepository(f.app, checker)

	assigned, ok, err := f.assignRoundRobin(t, repo, "round_robin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok || assigned != userID {
		t.Fatalf("expected the online candidate to be assigned, got ok=%v assigned=%v", ok, assigned)
	}
	if checker.callCount() != 1 {
		t.Fatalf("expected exactly 1 presence batch call for a single-candidate queue, got %d", checker.callCount())
	}
}

func TestAssignRoundRobin_FlagOn_OfflineCandidate_IsSkipped(t *testing.T) {
	f := newPresenceRoutingFixture(t, true)
	// offlineUser is ordered first (earlier last_assigned_at); onlineUser second.
	_, offlineAgentProfile := f.addAgent(t, at(-100))
	onlineUser, onlineAgentProfile := f.addAgent(t, at(-50))
	checker := &fakePresenceChecker{online: map[uuid.UUID]bool{offlineAgentProfile: false, onlineAgentProfile: true}}
	repo := NewPostgresAssignmentRepository(f.app, checker)

	assigned, ok, err := f.assignRoundRobin(t, repo, "round_robin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok || assigned != onlineUser {
		t.Fatalf("expected the online candidate (second in order) to be assigned, got ok=%v assigned=%v", ok, assigned)
	}
}

// runChunkBoundaryCase seeds total candidates (all offline except the one at
// onlineIndex, 0-based), asserts that exact candidate is the one assigned,
// and asserts the DB was paged in exactly wantPages fetches (proving no
// candidate is skipped or re-checked across page boundaries, and that the
// DB is queried incrementally — one page at a time — rather than all at
// once).
func runChunkBoundaryCase(t *testing.T, total, onlineIndex, wantPages int) {
	t.Helper()
	f := newPresenceRoutingFixture(t, true)
	online := map[uuid.UUID]bool{}
	var wantUser uuid.UUID
	for i := 0; i < total; i++ {
		userID, agentProfileID := f.addAgent(t, at(-100000+i)) // strictly increasing order
		online[agentProfileID] = i == onlineIndex
		if i == onlineIndex {
			wantUser = userID
		}
	}
	checker := &fakePresenceChecker{online: online}
	repo := NewPostgresAssignmentRepository(f.app, checker)

	assigned, ok, err := f.assignRoundRobin(t, repo, "round_robin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok || assigned != wantUser {
		t.Fatalf("expected candidate #%d (only one online, of %d) to be assigned, got ok=%v assigned=%v", onlineIndex+1, total, ok, assigned)
	}
	if checker.callCount() != wantPages {
		t.Fatalf("expected exactly %d presence batch calls for %d candidates, got %d", wantPages, total, checker.callCount())
	}
}

func TestAssignRoundRobin_FlagOn_ChunkBoundary_Candidate51Online(t *testing.T) {
	runChunkBoundaryCase(t, 51, 50, 2) // page of 50 (all offline) + page of 1 (online)
}

func TestAssignRoundRobin_FlagOn_ChunkBoundary_Candidate101Online(t *testing.T) {
	runChunkBoundaryCase(t, 101, 100, 3) // 50 + 50 (both offline) + 1 (online)
}

// TestAssignRoundRobin_FlagOn_NoDuplicateOrSkipAcrossPages seeds exactly two
// full pages (100 candidates) with a distinct online candidate near the
// START of the second page (index 50, i.e. candidate #51) — if the keyset
// cursor skipped or re-fetched a row when advancing between pages, either
// candidate #51 would be missed (skip) or the first page would be
// re-consulted needlessly (duplicate call count would exceed 2).
func TestAssignRoundRobin_FlagOn_NoDuplicateOrSkipAcrossPages(t *testing.T) {
	runChunkBoundaryCase(t, 100, 50, 2)
}

func TestAssignRoundRobin_FlagOn_AllOffline_ReturnsNoEligibleAgent(t *testing.T) {
	f := newPresenceRoutingFixture(t, true)
	_, ap1 := f.addAgent(t, nil)
	_, ap2 := f.addAgent(t, nil)
	checker := &fakePresenceChecker{online: map[uuid.UUID]bool{ap1: false, ap2: false}}
	repo := NewPostgresAssignmentRepository(f.app, checker)

	_, ok, err := f.assignRoundRobin(t, repo, "round_robin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected no assignment when every candidate is offline")
	}
}

// TestAssignRoundRobin_FlagOn_AllOfflineAcrossMultiplePages_ExhaustsAllPages
// proves exhaustion is only declared after every real DB page has been
// walked — not after the first page — by seeding 101 all-offline candidates
// (3 pages: 50+50+1) and asserting all 3 presence batch calls happened.
func TestAssignRoundRobin_FlagOn_AllOfflineAcrossMultiplePages_ExhaustsAllPages(t *testing.T) {
	f := newPresenceRoutingFixture(t, true)
	online := map[uuid.UUID]bool{}
	for i := 0; i < 101; i++ {
		_, agentProfileID := f.addAgent(t, at(-100000+i))
		online[agentProfileID] = false
	}
	checker := &fakePresenceChecker{online: online}
	repo := NewPostgresAssignmentRepository(f.app, checker)

	_, ok, err := f.assignRoundRobin(t, repo, "round_robin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected no assignment when every candidate across every page is offline")
	}
	if checker.callCount() != 3 {
		t.Fatalf("expected exactly 3 presence batch calls (50+50+1) proving full DB exhaustion, got %d", checker.callCount())
	}
}

func TestAssignRoundRobin_FlagOn_PresenceError_ReturnsErrPresenceUnavailable(t *testing.T) {
	f := newPresenceRoutingFixture(t, true)
	f.addAgent(t, nil)
	checker := &fakePresenceChecker{err: errors.New("valkey: connection refused")}
	repo := NewPostgresAssignmentRepository(f.app, checker)

	_, ok, err := f.assignRoundRobin(t, repo, "round_robin")
	if ok {
		t.Fatal("must never assign anything when the presence backend is unavailable")
	}
	if !errors.Is(err, ports.ErrPresenceUnavailable) {
		t.Fatalf("expected ports.ErrPresenceUnavailable, got %v", err)
	}
}

func TestAssignRoundRobin_FlagOn_PresenceCheckerNotConfigured_FailsClosed(t *testing.T) {
	f := newPresenceRoutingFixture(t, true)
	f.addAgent(t, nil)
	repo := NewPostgresAssignmentRepository(f.app, nil) // Valkey never configured for this process

	_, ok, err := f.assignRoundRobin(t, repo, "round_robin")
	if ok {
		t.Fatal("must never assign anything when no presence checker is configured")
	}
	if !errors.Is(err, ports.ErrPresenceUnavailable) {
		t.Fatalf("expected ports.ErrPresenceUnavailable, got %v", err)
	}
}

func TestAssignRoundRobin_FlagOff_PresenceUnavailableIsIrrelevant(t *testing.T) {
	// Same as FlagOff_NeverCallsPresence, but explicit about the failure-mode
	// matrix cell "flag OFF + Valkey down -> routing normal": passing nil
	// (no presence backend at all) must not affect a flag-off tenant.
	f := newPresenceRoutingFixture(t, false)
	userID, _ := f.addAgent(t, nil)
	repo := NewPostgresAssignmentRepository(f.app, nil)

	assigned, ok, err := f.assignRoundRobin(t, repo, "round_robin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok || assigned != userID {
		t.Fatalf("expected normal assignment despite no presence backend configured, got ok=%v assigned=%v", ok, assigned)
	}
}

func TestAssignRoundRobin_TenantFlagIsIndependent(t *testing.T) {
	fOn := newPresenceRoutingFixture(t, true)
	fOff := newPresenceRoutingFixture(t, false)
	_, apOn := fOn.addAgent(t, nil)
	userOff, _ := fOff.addAgent(t, nil)

	checkerOn := &fakePresenceChecker{online: map[uuid.UUID]bool{apOn: false}} // offline on the flag-ON tenant
	repoOn := NewPostgresAssignmentRepository(fOn.app, checkerOn)
	_, ok, err := fOn.assignRoundRobin(t, repoOn, "round_robin")
	if err != nil {
		t.Fatalf("tenant A: unexpected error: %v", err)
	}
	if ok {
		t.Fatal("tenant A (flag ON, candidate offline): expected no assignment")
	}

	repoOff := NewPostgresAssignmentRepository(fOff.app, panicPresenceChecker{t: t})
	assigned, ok, err := fOff.assignRoundRobin(t, repoOff, "round_robin")
	if err != nil {
		t.Fatalf("tenant B: unexpected error: %v", err)
	}
	if !ok || assigned != userOff {
		t.Fatalf("tenant B (flag OFF): expected normal assignment unaffected by tenant A, got ok=%v assigned=%v", ok, assigned)
	}
}

func TestAssignRoundRobin_FlagOn_ConcurrentCallsHaveExactlyOneWinner(t *testing.T) {
	f := newPresenceRoutingFixture(t, true)
	userID, agentProfileID := f.addAgent(t, nil)
	checker := &fakePresenceChecker{online: map[uuid.UUID]bool{agentProfileID: true}}
	repo := NewPostgresAssignmentRepository(f.app, checker)

	results := make(chan uuid.UUID, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assigned, _, err := f.assignRoundRobin(t, repo, "round_robin")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			results <- assigned
		}()
	}
	wg.Wait()
	close(results)
	for r := range results {
		if r != userID {
			t.Fatalf("expected every caller to observe the same single winner %s, got %s", userID, r)
		}
	}
	if got := f.assignedUser(t); got == nil || *got != userID {
		t.Fatalf("expected exactly one durable assignment to %s, got %v", userID, got)
	}
}

func TestAssignRoundRobin_FlagOn_ManualClaimStillWorksOffline(t *testing.T) {
	f := newPresenceRoutingFixture(t, true)
	userID, agentProfileID := f.addAgent(t, nil)
	checker := &fakePresenceChecker{online: map[uuid.UUID]bool{agentProfileID: false}}
	repo := NewPostgresAssignmentRepository(f.app, checker)

	// Manual claim is a completely different method — never presence-gated,
	// regardless of routing_require_presence.
	var claimed bool
	err := platformdb.WithTenantSession(context.Background(), f.app, userID, false, func(sctx context.Context) error {
		tc, tcErr := tenancydomain.NewTenantContext(f.tenantID, userID, tenancydomain.AccessSourceDirect)
		if tcErr != nil {
			return tcErr
		}
		ctx := tenancydomain.WithTenantContext(sctx, tc)
		var claimErr error
		claimed, claimErr = repo.ClaimUnassigned(ctx, f.conversationID, userID, "manual_claim")
		return claimErr
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !claimed {
		t.Fatal("expected manual claim to succeed for an offline agent regardless of routing_require_presence")
	}
	if checker.callCount() != 0 {
		t.Fatalf("manual claim must never consult presence, got %d calls", checker.callCount())
	}
}
