package adapters

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/contextwindow"
	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// CONTEXT.2A: storage and scheduling for grouped semantic analysis. Real Postgres, FORCE RLS, runtime role
// (omnira_app) — never the owner connection, except through e.seed for setup/assertions.

func testPolicy() contextwindow.Policy {
	return contextwindow.Policy{Idle: 30 * time.Second, MaxAge: 2 * time.Minute, MaxMessages: 3}
}

func windowRow(e *env, id uuid.UUID) (state string, count int, generation int, trigger *string) {
	e.t.Helper()
	if err := e.seed.QueryRow(e.ctx, `SELECT state, message_count, generation, trigger FROM context_analysis_windows WHERE id=$1`, id).
		Scan(&state, &count, &generation, &trigger); err != nil {
		e.t.Fatalf("windowRow: %v", err)
	}
	return
}

func openWindowID(e *env, tenant, conversation uuid.UUID) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT id FROM context_analysis_windows WHERE tenant_id=$1 AND conversation_id=$2 AND state='open'`, tenant, conversation).Scan(&id); err != nil {
		e.t.Fatalf("no open window: %v", err)
	}
	return id
}

func TestTouchOpensAWindowAndExtendsItUntilThePolicyClosesItByMaxMessages(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	policy := testPolicy() // MaxMessages: 3

	m1 := e.message(a.id, a.conversation, "oi")
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	id := openWindowID(e, a.id, a.conversation)
	state, count, gen, _ := windowRow(e, id)
	if state != "open" || count != 1 || gen != 1 {
		t.Fatalf("after 1st message: state=%s count=%d gen=%d", state, count, gen)
	}

	m2 := e.message(a.id, a.conversation, "tudo bem?")
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m2, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	state, count, _, _ = windowRow(e, id)
	if state != "open" || count != 2 {
		t.Fatalf("after 2nd message: state=%s count=%d, want open/2", state, count)
	}

	m3 := e.message(a.id, a.conversation, "preciso de ajuda")
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m3, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	state, count, _, _ = windowRow(e, id)
	if state != "due" || count != 3 {
		t.Fatalf("after 3rd message (max_messages=3): state=%s count=%d, want due/3", state, count)
	}

	// A 4th message must NOT reopen or extend the closed window: a new generation starts.
	m4 := e.message(a.id, a.conversation, "mais uma")
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m4, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	id2 := openWindowID(e, a.id, a.conversation)
	if id2 == id {
		t.Fatal("the 4th message must open a NEW window, not reopen the closed one")
	}
	_, count2, gen2, _ := windowRow(e, id2)
	if count2 != 1 || gen2 != 2 {
		t.Fatalf("new window: count=%d gen=%d, want 1/2", count2, gen2)
	}
}

func TestTouchIsIdempotentForARedeliveredMessage(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	policy := testPolicy()
	m1 := e.message(a.id, a.conversation, "oi")
	at := time.Now().UTC()
	for i := 0; i < 5; i++ { // the same event, delivered again and again
		if err := store.Touch(e.ctx, policy, a.id, a.conversation, m1, at); err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
	}
	id := openWindowID(e, a.id, a.conversation)
	_, count, _, _ := windowRow(e, id)
	if count != 1 {
		t.Fatalf("message_count = %d, want exactly 1 however many times the event arrives", count)
	}
	if n := e.count(`SELECT count(*) FROM context_analysis_window_messages WHERE window_id=$1`, id); n != 1 {
		t.Fatalf("membership rows = %d, want 1", n)
	}
}

// Regression for a Codex finding (round 1, CONTEXT.2D): a message redelivered after its original window already
// closed used to find no 'open' window and start a brand-new one, double-counting and later double-analyzing the
// same message. A message belongs to exactly one window ever (PRIMARY KEY (tenant_id, message_id) on
// context_analysis_window_messages, migration 000176).
func TestTouchRejectsAMessageRedeliveredAfterItsOriginalWindowAlreadyClosed(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	policy := testPolicy()
	m1 := e.message(a.id, a.conversation, "oi")
	at := time.Now().UTC()
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m1, at); err != nil {
		t.Fatal(err)
	}
	id1 := openWindowID(e, a.id, a.conversation)
	if _, err := store.Force(e.ctx, a.id, a.conversation, contextwindow.TriggerManual); err != nil {
		t.Fatal(err) // the window m1 belongs to is now 'due', not 'open'
	}

	// The same event, redelivered now that its window has closed.
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m1, at); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM context_analysis_windows WHERE tenant_id=$1 AND conversation_id=$2 AND state='open'`, a.id, a.conversation); n != 0 {
		t.Fatalf("open windows after redelivery = %d, want 0 (no new window for an already-processed message)", n)
	}
	if n := e.count(`SELECT count(*) FROM context_analysis_window_messages WHERE message_id=$1`, m1); n != 1 {
		t.Fatalf("membership rows for the redelivered message = %d, want exactly 1", n)
	}
	_, count, _, _ := windowRow(e, id1)
	if count != 1 {
		t.Fatalf("original window message_count = %d, want unchanged at 1", count)
	}
}

func TestTouchConcurrentOpenersOfTheSameConversationNeverCreateTwoOpenWindows(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	policy := testPolicy()

	const n = 8
	ids := make([]uuid.UUID, n)
	for i := range ids {
		ids[i] = e.message(a.id, a.conversation, "x")
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = store.Touch(e.ctx, policy, a.id, a.conversation, ids[i], time.Now().UTC())
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("touch %d: %v", i, err)
		}
	}
	if n := e.count(`SELECT count(*) FROM context_analysis_windows WHERE tenant_id=$1 AND conversation_id=$2 AND state='open'`, a.id, a.conversation); n != 1 {
		t.Fatalf("open windows = %d, want exactly 1 (no duplicate created by the race)", n)
	}
	// Every distinct message ended up in SOME window of this conversation (none lost).
	total := e.count(`SELECT count(*) FROM context_analysis_window_messages wm JOIN context_analysis_windows w ON w.id=wm.window_id WHERE w.tenant_id=$1 AND w.conversation_id=$2`, a.id, a.conversation)
	if total != n {
		t.Fatalf("messages recorded across windows = %d, want %d (max_messages=%d may have split them across generations, but none lost)", total, n, policy.MaxMessages)
	}
}

func TestTouchNeverLeaksAcrossTenantsOrConversations(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	otherConv := e.conversationFor(a.id, nil)
	store := NewPostgresContextWindowStore(e.app)
	policy := testPolicy()

	ma := e.message(a.id, a.conversation, "a")
	mb := e.message(b.id, b.conversation, "b")
	mOther := e.message(a.id, otherConv, "other conv, same tenant")
	for _, c := range []struct{ tenant, conv, msg uuid.UUID }{
		{a.id, a.conversation, ma}, {b.id, b.conversation, mb}, {a.id, otherConv, mOther},
	} {
		if err := store.Touch(e.ctx, policy, c.tenant, c.conv, c.msg, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count(`SELECT count(*) FROM context_analysis_windows WHERE tenant_id=$1`, a.id); n != 2 {
		t.Fatalf("tenant A windows = %d, want 2 (one per conversation)", n)
	}
	if n := e.count(`SELECT count(*) FROM context_analysis_windows WHERE tenant_id=$1`, b.id); n != 1 {
		t.Fatalf("tenant B windows = %d, want 1", n)
	}
	// Tenant B's member cannot see tenant A's windows at all (RLS).
	userB := e.member(b.id, "tenant_admin")
	e.session(b.id, userB, func(ctx context.Context) {
		var n int
		_ = platformdb.QuerierFromContext(ctx, e.app).QueryRow(ctx, `SELECT count(*) FROM context_analysis_windows`).Scan(&n)
		if n != 1 {
			t.Fatalf("tenant B session sees %d window(s), want 1 (its own only)", n)
		}
	})
}

func TestResolveMessageReadsTheTenantFromTheStoredRowNeverFromTheCaller(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	m1 := e.message(a.id, a.conversation, "oi")
	e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES(gen_random_uuid(),$1,$2,'outbound','text','saindo','sent')`, a.id, a.conversation)

	msg, found, err := store.ResolveMessage(e.ctx, m1)
	if err != nil || !found || msg.TenantID != a.id || msg.ConversationID != a.conversation {
		t.Fatalf("ResolveMessage(inbound) = %+v found=%v err=%v", msg, found, err)
	}
	if _, found, err := store.ResolveMessage(e.ctx, uuid.New()); err != nil || found {
		t.Fatalf("unknown message: found=%v err=%v, want false/nil", found, err)
	}
	var outboundID uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT id FROM messages WHERE tenant_id=$1 AND direction='outbound'`, a.id).Scan(&outboundID)
	if _, found, err := store.ResolveMessage(e.ctx, outboundID); err != nil || found {
		t.Fatalf("outbound message: found=%v err=%v, want false/nil (never windowed)", found, err)
	}
}

// Regression for a Codex finding (round 9, CONTEXT.2D): an internal (staff-to-staff) conversation has no contact
// (ADR-0018) and is never customer content — TopicRepository.ConversationInfo already excludes it, but
// ResolveMessage is a SEPARATE path (the consumer's own decision whether to open/extend a window at all) that
// never applied the same guard. Without this, an internal conversation's inbound message would enter the
// customer-context pipeline and, with a model configured, get analyzed/labeled "cliente".
func TestResolveMessageExcludesInternalStaffConversations(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	staffer := e.member(a.id, "tenant_agent")
	internalConv := uuid.New()
	e.exec(`INSERT INTO conversations (id, tenant_id, status, conversation_kind, internal_user_id) VALUES ($1,$2,'open','internal',$3)`,
		internalConv, a.id, staffer)
	m1 := uuid.New()
	e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES($1,$2,$3,'inbound','text','mensagem interna','received')`,
		m1, a.id, internalConv)

	if _, found, err := store.ResolveMessage(e.ctx, m1); err != nil || found {
		t.Fatalf("internal conversation's message: found=%v err=%v, want false/nil (never enters the customer-context pipeline)", found, err)
	}
}

func TestForceClosesTheOpenWindowImmediatelyAndIsANoOpWithoutOne(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	policy := testPolicy()

	if forced, err := store.Force(e.ctx, a.id, a.conversation, contextwindow.TriggerClaim); err != nil || forced {
		t.Fatalf("force without an open window: forced=%v err=%v, want false/nil", forced, err)
	}
	m1 := e.message(a.id, a.conversation, "oi")
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	id := openWindowID(e, a.id, a.conversation)
	forced, err := store.Force(e.ctx, a.id, a.conversation, contextwindow.TriggerHandoff)
	if err != nil || !forced {
		t.Fatalf("force: forced=%v err=%v, want true/nil", forced, err)
	}
	state, count, _, trig := windowRow(e, id)
	if state != "due" || count != 1 || trig == nil || *trig != "handoff" {
		t.Fatalf("after force: state=%s count=%d trigger=%v, want due/1/handoff", state, count, trig)
	}
}

// Regression for a Codex finding (round 8, CONTEXT.2D): Force's real caller, the manual-refresh HTTP handler,
// runs inside the REQUEST's own member-level session (no system privilege at all) — Force used to unconditionally
// open ANOTHER, system-level session on top of it, a second nested pool connection that could stall every
// refresh on a small pool. force_context_window (migration 000176) is SECURITY DEFINER, so calling it through
// the member session's OWN connection still succeeds — proving no escalation, and no second connection, is
// needed.
func TestForceSucceedsThroughAnAlreadyActiveMemberSessionWithNoSystemPrivilege(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	policy := testPolicy()
	m1 := e.message(a.id, a.conversation, "oi")
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	id := openWindowID(e, a.id, a.conversation)

	agent := e.member(a.id, "tenant_agent")
	e.exec(`UPDATE conversations SET assigned_to_user_id=$1 WHERE tenant_id=$2 AND id=$3`, agent, a.id, a.conversation) // the attendant check force_context_window itself now does (round 9)
	var forced bool
	var err error
	e.session(a.id, agent, func(ctx context.Context) {
		forced, err = store.Force(ctx, a.id, a.conversation, contextwindow.TriggerManual)
	})
	if err != nil || !forced {
		t.Fatalf("force through a member session: forced=%v err=%v, want true/nil", forced, err)
	}
	state, _, _, trig := windowRow(e, id)
	if state != "due" || trig == nil || *trig != "manual" {
		t.Fatalf("after force: state=%s trigger=%v, want due/manual", state, trig)
	}
}

// Regression for a Codex finding (round 9, CONTEXT.2D): force_context_window is SECURITY DEFINER, reachable by
// ANY omnira_app session — GRANT EXECUTE authorizes nothing by itself. A member who holds neither topic.manage
// nor is the conversation's attendant must be refused, exactly like the HTTP handler's own gate would refuse
// them, as a database-level backstop independent of whatever the Go layer did or didn't check.
func TestForceRefusesAMemberWithoutTopicManageOrWhoIsNotTheAttendant(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	policy := testPolicy()
	m1 := e.message(a.id, a.conversation, "oi")
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	id := openWindowID(e, a.id, a.conversation)

	stranger := e.readOnlyMember(a.id) // tenant.read only: no topic.manage, not the attendant
	var forced bool
	var err error
	e.session(a.id, stranger, func(ctx context.Context) {
		forced, err = store.Force(ctx, a.id, a.conversation, contextwindow.TriggerManual)
	})
	if err != nil || forced {
		t.Fatalf("force without topic.manage or being the attendant: forced=%v err=%v, want false/nil", forced, err)
	}
	state, _, _, _ := windowRow(e, id)
	if state != "open" {
		t.Fatalf("state = %s, want still open (an unauthorized force must change nothing)", state)
	}
}

// Same finding, the actual cross-Tenant case Codex described: a member of tenant B must never force tenant A's
// window, regardless of what p_tenant_id/p_conversation_id the caller passes.
func TestForceRefusesAMemberOfAnotherTenant(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	policy := testPolicy()
	m1 := e.message(a.id, a.conversation, "oi")
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	id := openWindowID(e, a.id, a.conversation)

	stranger := e.member(b.id, "tenant_agent") // a member of B, not A
	var forced bool
	var err error
	e.session(b.id, stranger, func(ctx context.Context) {
		forced, err = store.Force(ctx, a.id, a.conversation, contextwindow.TriggerManual)
	})
	if err != nil || forced {
		t.Fatalf("a member of another tenant must never force A's window: forced=%v err=%v, want false/nil", forced, err)
	}
	state, _, _, _ := windowRow(e, id)
	if state != "open" {
		t.Fatalf("state = %s, want still open", state)
	}
}

func TestSweepClosesOnlyWindowsPastTheirDueAtAndIsIdempotent(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	policy := testPolicy()
	m1 := e.message(a.id, a.conversation, "oi")
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	id := openWindowID(e, a.id, a.conversation)

	if n, err := store.Sweep(e.ctx, time.Now().UTC(), 100); err != nil || n != 0 {
		t.Fatalf("sweep before due_at: n=%d err=%v, want 0/nil (idle=%s has not elapsed)", n, err, policy.Idle)
	}
	state, _, _, _ := windowRow(e, id)
	if state != "open" {
		t.Fatalf("state = %s, want still open", state)
	}

	future := time.Now().UTC().Add(policy.Idle + time.Second)
	if n, err := store.Sweep(e.ctx, future, 100); err != nil || n != 1 {
		t.Fatalf("sweep past due_at: n=%d err=%v, want 1/nil", n, err)
	}
	state, _, _, _ = windowRow(e, id)
	if state != "due" {
		t.Fatalf("state = %s, want due", state)
	}
	if n, err := store.Sweep(e.ctx, future, 100); err != nil || n != 0 {
		t.Fatalf("sweeping again: n=%d err=%v, want 0/nil (already due, not open)", n, err)
	}
}

func TestClaimLeasesWithSkipLockedAndRecoversAnExpiredLease(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	policy := testPolicy()
	m1 := e.message(a.id, a.conversation, "oi")
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Force(e.ctx, a.id, a.conversation, contextwindow.TriggerManual); err != nil {
		t.Fatal(err)
	}

	// A 50ms lease (and an 80ms sleep past it) was too tight: under a loaded CI container, the round trip for the
	// "still live" claim below could itself take tens of ms, so the lease could expire between the two claims and
	// make the test fail on a real recovery, not a bug. 300ms/450ms keeps the same two-phase check (live, then
	// expired) with real margin on both sides.
	claimed, err := store.Claim(e.ctx, 10, 300*time.Millisecond)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("1st claim: n=%d err=%v, want 1/nil", len(claimed), err)
	}
	w := claimed[0]
	if w.Attempt != 1 || w.ConversationID != a.conversation {
		t.Fatalf("claimed window = %+v", w)
	}
	// A second claim right away sees nothing: the lease has not expired and nothing else is due.
	if again, err := store.Claim(e.ctx, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("2nd claim (lease live): n=%d err=%v, want 0/nil", len(again), err)
	}
	time.Sleep(450 * time.Millisecond) // past the 300ms lease
	recovered, err := store.Claim(e.ctx, 10, time.Minute)
	if err != nil || len(recovered) != 1 || recovered[0].Attempt != 2 {
		t.Fatalf("recovery claim: n=%d attempt=%v err=%v, want 1/2/nil", len(recovered), recovered, err)
	}
	// The stale first claim can no longer complete it (fenced by attempt): it already moved on.
	if ok, err := store.Complete(e.ctx, w); err != nil || ok {
		t.Fatalf("stale Complete: ok=%v err=%v, want false/nil (fenced out by generation/attempt)", ok, err)
	}
	if ok, err := store.Complete(e.ctx, recovered[0]); err != nil || !ok {
		t.Fatalf("current Complete: ok=%v err=%v, want true/nil", ok, err)
	}
}

// Regression for a Codex finding (round 1, CONTEXT.2D): Claim used to pick up every 'due' window regardless of
// due_at, so Retry's exponential backoff was defeated by the worker's own next poll tick.
func TestClaimHonorsRetryBackoffAndDoesNotPickUpABusyWindowEarly(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	policy := testPolicy()
	m1 := e.message(a.id, a.conversation, "oi")
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Force(e.ctx, a.id, a.conversation, contextwindow.TriggerManual); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(e.ctx, 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: n=%d err=%v", len(claimed), err)
	}
	const backoff = 150 * time.Millisecond
	if ok, err := store.Retry(e.ctx, claimed[0], "transient", backoff); err != nil || !ok {
		t.Fatalf("retry: ok=%v err=%v, want true/nil", ok, err)
	}
	// due_at is still in the future right after the retry: Claim must not pick it up.
	if again, err := store.Claim(e.ctx, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("claim before backoff elapsed: n=%d err=%v, want 0/nil", len(again), err)
	}
	time.Sleep(backoff + 100*time.Millisecond)
	after, err := store.Claim(e.ctx, 10, time.Minute)
	if err != nil || len(after) != 1 {
		t.Fatalf("claim after backoff elapsed: n=%d err=%v, want 1/nil", len(after), err)
	}
}

func TestClaimTwoWorkersNeverGetTheSameWindow(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	policy := testPolicy()
	const n = 6
	for i := 0; i < n; i++ {
		conv := e.conversationFor(a.id, nil)
		m := e.message(a.id, conv, "x")
		if err := store.Touch(e.ctx, policy, a.id, conv, m, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Force(e.ctx, a.id, conv, contextwindow.TriggerPriority); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make([][]ports.ContextWindow, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := store.Claim(e.ctx, n, time.Minute)
			if err != nil {
				t.Errorf("worker %d: %v", i, err)
				return
			}
			results[i] = got
		}(i)
	}
	wg.Wait()
	seen := map[uuid.UUID]bool{}
	total := 0
	for _, r := range results {
		for _, w := range r {
			if seen[w.ID] {
				t.Fatalf("window %s claimed by more than one worker", w.ID)
			}
			seen[w.ID] = true
			total++
		}
	}
	if total != n {
		t.Fatalf("total claimed = %d, want %d (every due window claimed exactly once)", total, n)
	}
}

func TestRetryReschedulesAndDeadIsPermanentFencedByAttempt(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	policy := testPolicy()
	m1 := e.message(a.id, a.conversation, "oi")
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Force(e.ctx, a.id, a.conversation, contextwindow.TriggerManual); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(e.ctx, 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: n=%d err=%v", len(claimed), err)
	}
	w := claimed[0]
	if ok, err := store.Retry(e.ctx, w, "transient", time.Millisecond); err != nil || !ok {
		t.Fatalf("retry: ok=%v err=%v, want true/nil", ok, err)
	}
	state, _, _, _ := windowRow(e, w.ID)
	if state != "due" {
		t.Fatalf("after retry: state=%s, want due", state)
	}
	// Retrying again with the SAME (now stale) attempt token must fail: the window already moved past it.
	if ok, err := store.Retry(e.ctx, w, "transient", time.Millisecond); err != nil || ok {
		t.Fatalf("stale retry: ok=%v err=%v, want false/nil", ok, err)
	}

	claimed2, err := store.Claim(e.ctx, 1, time.Minute)
	if err != nil || len(claimed2) != 1 {
		t.Fatalf("2nd claim: n=%d err=%v", len(claimed2), err)
	}
	if ok, err := store.Dead(e.ctx, claimed2[0], "permanent"); err != nil || !ok {
		t.Fatalf("dead: ok=%v err=%v, want true/nil", ok, err)
	}
	state, _, _, _ = windowRow(e, w.ID)
	if state != "dead" {
		t.Fatalf("after dead: state=%s, want dead", state)
	}
	// Dead is terminal: Claim never picks it up again, however long it waits.
	if n, err := store.Sweep(e.ctx, time.Now().UTC().Add(time.Hour), 100); err != nil || n != 0 {
		t.Fatalf("sweep after dead: n=%d err=%v, want 0/nil", n, err)
	}
	if again, err := store.Claim(e.ctx, 10, time.Nanosecond); err != nil || len(again) != 0 {
		t.Fatalf("claim after dead: n=%d err=%v, want 0/nil", len(again), err)
	}
}

func TestMessageIDsReturnsTheWindowMembershipOldestFirst(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	policy := contextwindow.Policy{Idle: 30 * time.Second, MaxAge: 2 * time.Minute, MaxMessages: 10}
	// Close to the real "now" Touch evaluates the policy against: a message far in the past relative to actual
	// wall-clock time would close the window immediately via the idle/max-age path, before the other 3 could join
	// it. Still deliberately out of chronological order by a few milliseconds, to prove the cursor sorts by
	// message_at, not insertion order.
	base := time.Now().UTC()
	ids := make([]uuid.UUID, 4)
	for i := range ids {
		ids[i] = e.message(a.id, a.conversation, "m")
		at := base.Add(time.Duration(len(ids)-i) * 10 * time.Millisecond)
		if err := store.Touch(e.ctx, policy, a.id, a.conversation, ids[i], at); err != nil {
			t.Fatal(err)
		}
	}
	window := ports.ContextWindow{TenantID: a.id, ID: openWindowID(e, a.id, a.conversation)}
	got, err := store.MessageIDs(e.ctx, window)
	if err != nil || len(got) != 4 {
		t.Fatalf("MessageIDs: n=%d err=%v", len(got), err)
	}
	for i, id := range got {
		if id != ids[len(ids)-1-i] {
			t.Fatalf("order[%d] = %s, want the reverse insertion order (oldest message_at first)", i, id)
		}
	}
}

// Regression for a Codex finding (round 4, CONTEXT.2D): MessageIDs used to always open its OWN nested system
// session, acquiring a SECOND pool connection on top of whatever session the caller (WindowAnalyzerService.Analyze,
// itself inside the runner's session) already holds — with ClaimLimit analyses running at once, that alone can
// exhaust a pool no bigger than the claim batch and deadlock the runner. Called from WITHIN an existing session
// (via sys, mirroring how the runner's TenantSession wraps Analyze), it must reuse that session's own querier
// instead, with no second connection acquired.
func TestMessageIDsReusesAnAlreadyActiveSessionInsteadOfOpeningANestedOne(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresContextWindowStore(e.app)
	policy := contextwindow.DefaultPolicy()
	m1 := e.message(a.id, a.conversation, "m")
	if err := store.Touch(e.ctx, policy, a.id, a.conversation, m1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	window := ports.ContextWindow{TenantID: a.id, ID: openWindowID(e, a.id, a.conversation)}

	got, err := sys(e, a.id, func(ctx context.Context) ([]uuid.UUID, error) { return store.MessageIDs(ctx, window) })
	if err != nil || len(got) != 1 || got[0] != m1 {
		t.Fatalf("MessageIDs within an active session: got=%v err=%v", got, err)
	}
}
