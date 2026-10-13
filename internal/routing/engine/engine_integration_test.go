package engine_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	routingadapters "github.com/omnira/omnira/internal/routing/adapters"
	"github.com/omnira/omnira/internal/routing/engine"
	"github.com/omnira/omnira/internal/routing/ports"
	sectorsadapters "github.com/omnira/omnira/internal/sectors/adapters"
	"github.com/omnira/omnira/internal/testhelpers"
)

// Real Postgres, runtime role under RLS, the same system tenant session the routing worker uses.

type env struct {
	ctx       context.Context
	seed, app *pgxpool.Pool
	eng       *engine.Engine
}

type fakePresence struct {
	mu     sync.Mutex
	online map[uuid.UUID]bool
	fail   bool
}

func (f *fakePresence) OnlineMembers(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("valkey down")
	}
	out := map[uuid.UUID]bool{}
	for _, id := range ids {
		out[id] = f.online[id]
	}
	return out, nil
}

func newEnv(t *testing.T, presence sectorsadapters.PresenceChecker) *env {
	t.Helper()
	owner, app := testhelpers.RequireIntegrationDatabase(t)
	open := func(url string) *pgxpool.Pool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p, err := pgxpool.New(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Ping(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	e := &env{ctx: context.Background(), seed: open(owner), app: open(app)}
	e.eng = engine.New(e.app, presence)
	return e
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.seed.Exec(e.ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.seed.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (e *env) tenant(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	e.exec(t, `INSERT INTO tenants (id, legal_name, isolation_profile, status) VALUES ($1,$2,'shared_strong_isolation','active')`, id, "eng-"+id.String()[:8])
	t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, id) })
	return id
}

func (e *env) queue(t *testing.T, tenant uuid.UUID, name, mode string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	e.exec(t, `INSERT INTO queues (id, tenant_id, name, mode, is_default) VALUES ($1,$2,$3,$4,false)`, id, tenant, name, mode)
	return id
}

// attendant: an active member with an active profile; a member of the queue when queue != nil.
func (e *env) attendant(t *testing.T, tenant uuid.UUID, queue *uuid.UUID, capacity int) (user, profile uuid.UUID) {
	t.Helper()
	user = uuid.New()
	e.exec(t, `INSERT INTO users (id, external_subject, email, display_name, status) VALUES ($1,$2,$3,'a','active')`, user, user.String(), user.String()+"@example.com")
	t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user) })
	var role uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL LIMIT 1`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	m := uuid.New()
	e.exec(t, `INSERT INTO memberships (id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,'active')`, m, tenant, user, role)
	profile = uuid.New()
	e.exec(t, `INSERT INTO agent_profiles (id, tenant_id, membership_id, status) VALUES ($1,$2,$3,'active')`, profile, tenant, m)
	if queue != nil {
		e.exec(t, `INSERT INTO queue_members (tenant_id, queue_id, user_id, active, available, capacity) VALUES ($1,$2,$3,true,true,$4)`, tenant, *queue, user, capacity)
	}
	return user, profile
}

var phoneSeq uint64

// phone: a valid, unique E.164 number per call
func phone() string {
	phoneSeq++
	return fmt.Sprintf("+55929%08d", (uint64(time.Now().UnixNano())/1000+phoneSeq)%100000000)
}

func (e *env) conversation(t *testing.T, tenant uuid.UUID, queue *uuid.UUID) uuid.UUID {
	t.Helper()
	contact, conv := uuid.New(), uuid.New()
	e.exec(t, `INSERT INTO contacts (id, tenant_id, display_name, phone_e164, status) VALUES ($1,$2,'C',$3,'active')`, contact, tenant, phone())
	e.exec(t, `INSERT INTO conversations (id, tenant_id, contact_id, queue_id, status) VALUES ($1,$2,$3,$4,'open')`, conv, tenant, contact, queue)
	return conv
}

func (e *env) calendar(t *testing.T, tenant uuid.UUID, name, kind, weekly string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	e.exec(t, `INSERT INTO calendars (id, tenant_id, name, timezone, kind, weekly) VALUES ($1,$2,$3,'America/Manaus',$4,$5::jsonb)`, id, tenant, name, kind, weekly)
	return id
}

func (e *env) decide(t *testing.T, tenant, conv uuid.UUID, trigger string) (engine.Decision, error) {
	t.Helper()
	var d engine.Decision
	err := platformdb.WithSystemTenantSession(e.ctx, e.app, tenant, func(c context.Context) error {
		var err error
		d, err = e.eng.Decide(c, engine.Request{ConversationID: conv, Trigger: trigger, StrategyReason: "round_robin"})
		return err
	})
	return d, err
}

func (e *env) owner(t *testing.T, conv uuid.UUID) *uuid.UUID {
	t.Helper()
	var o *uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT assigned_to_user_id FROM conversations WHERE id=$1`, conv).Scan(&o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestManualClosedAndAssignedConversationsAreLeftAlone(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	manual := e.queue(t, a, "Manual", "manual")
	rr := e.queue(t, a, "RR", "round_robin")
	u, _ := e.attendant(t, a, &rr, 3)
	cm := e.conversation(t, a, &manual)
	if d, err := e.decide(t, a, cm, ""); err != nil || d.Kind != engine.Enqueue || d.Reason != engine.ReasonManualQueue || e.owner(t, cm) != nil {
		t.Fatalf("manual queue: %+v %v", d, err)
	}
	noQueue := e.conversation(t, a, nil)
	if d, _ := e.decide(t, a, noQueue, ""); d.Kind != engine.NoAction || d.Reason != engine.ReasonNoQueue {
		t.Fatalf("no queue: %+v", d)
	}
	closed := e.conversation(t, a, &rr)
	e.exec(t, `UPDATE conversations SET status='closed' WHERE id=$1`, closed)
	if d, _ := e.decide(t, a, closed, ""); d.Kind != engine.NoAction || d.Reason != engine.ReasonClosed || e.owner(t, closed) != nil {
		t.Fatalf("closed: %+v", d)
	}
	// somebody already owns it: never reassigned, no new history
	taken := e.conversation(t, a, &rr)
	e.exec(t, `UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, taken, u)
	d, err := e.decide(t, a, taken, "")
	if err != nil || d.Kind != engine.AssignAgent || d.Reason != engine.ReasonAlreadyAssigned || d.AssignedUser != u {
		t.Fatalf("already assigned: %+v %v", d, err)
	}
	if e.count(t, `SELECT count(*) FROM assignment_events WHERE conversation_id=$1`, taken) != 0 {
		t.Fatal("an existing owner must not produce a new assignment event")
	}
	// another tenant's conversation is simply not found, and a non-system context is refused
	b := e.tenant(t)
	if _, err := e.decide(t, b, cm, ""); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("conversation of another tenant: %v", err)
	}
	err = platformdb.WithTenantSession(e.ctx, e.app, u, false, func(c context.Context) error {
		_, err := e.eng.Decide(c, engine.Request{ConversationID: cm})
		return err
	})
	if err == nil {
		t.Fatal("a request without a system tenant context must be refused")
	}
}

// The engine must pick, conversation after conversation, exactly the agents the legacy round-robin picks.
func TestRoundRobinOrderMatchesTheLegacyRepository(t *testing.T) {
	e := newEnv(t, nil)
	legacyT, engineT := e.tenant(t), e.tenant(t)
	lq, eq := e.queue(t, legacyT, "RR", "round_robin"), e.queue(t, engineT, "RR", "round_robin")
	// the SAME people are members of both companies, so the user-id tie-break is identical
	var users []uuid.UUID
	for i := 0; i < 3; i++ {
		u, _ := e.attendant(t, legacyT, &lq, 5)
		users = append(users, u)
		var role uuid.UUID
		_ = e.seed.QueryRow(e.ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL LIMIT 1`).Scan(&role)
		m := uuid.New()
		e.exec(t, `INSERT INTO memberships (id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,'active')`, m, engineT, u, role)
		e.exec(t, `INSERT INTO agent_profiles (tenant_id, membership_id, status) VALUES ($1,$2,'active')`, engineT, m)
		e.exec(t, `INSERT INTO queue_members (tenant_id, queue_id, user_id, active, available, capacity) VALUES ($1,$2,$3,true,true,5)`, engineT, eq, u)
	}
	legacy := routingadapters.NewPostgresAssignmentRepository(e.app, nil)
	var want, got []uuid.UUID
	for i := 0; i < 7; i++ {
		lc, ec := e.conversation(t, legacyT, &lq), e.conversation(t, engineT, &eq)
		err := platformdb.WithSystemTenantSession(e.ctx, e.app, legacyT, func(c context.Context) error {
			u, ok, err := legacy.AssignRoundRobin(c, lc, "round_robin")
			if err != nil || !ok {
				return errors.New("legacy did not assign")
			}
			want = append(want, u)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		d, err := e.decide(t, engineT, ec, "")
		if err != nil || d.Kind != engine.AssignAgent {
			t.Fatalf("engine: %+v %v", d, err)
		}
		got = append(got, d.AssignedUser)
		time.Sleep(5 * time.Millisecond) // last_assigned_at must differ between steps
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("assignment %d: legacy picked %s, engine picked %s\nlegacy %v\nengine %v", i, want[i], got[i], want, got)
		}
	}
	if len(users) != 3 {
		t.Fatal("fixture")
	}
}

func TestLeastLoadedPicksTheFewestOpenConversationsThenTheOldestAssignment(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "LL", "least_loaded")
	busy, _ := e.attendant(t, a, &q, 10)
	light, _ := e.attendant(t, a, &q, 10)
	idle, _ := e.attendant(t, a, &q, 10)
	hold := func(u uuid.UUID, n int) {
		for i := 0; i < n; i++ {
			c := e.conversation(t, a, &q)
			e.exec(t, `UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, c, u)
		}
	}
	hold(busy, 4)
	hold(light, 1)
	c1 := e.conversation(t, a, &q)
	if d, err := e.decide(t, a, c1, ""); err != nil || d.AssignedUser != idle {
		t.Fatalf("the agent with no open conversations goes first: %+v %v", d, err)
	}
	c2 := e.conversation(t, a, &q) // idle now has 1, light has 1: the one assigned longest ago (never) wins -> light
	if d, _ := e.decide(t, a, c2, ""); d.AssignedUser != light {
		t.Fatalf("tie on load: the one never assigned goes first, got %v", d.AssignedUser)
	}
	c3 := e.conversation(t, a, &q) // idle 1, light 2, busy 4 -> idle
	if d, _ := e.decide(t, a, c3, ""); d.AssignedUser != idle {
		t.Fatalf("lowest load again: %v", d.AssignedUser)
	}
}

func TestCapacityExhaustedEnqueuesAndTheSameDecisionRowIsUpdatedNotDuplicated(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "RR", "round_robin")
	u, _ := e.attendant(t, a, &q, 1)
	first := e.conversation(t, a, &q)
	if d, _ := e.decide(t, a, first, ""); d.AssignedUser != u {
		t.Fatalf("first: %+v", d)
	}
	second := e.conversation(t, a, &q)
	for i := 1; i <= 3; i++ {
		d, err := e.decide(t, a, second, "")
		if err != nil || d.Kind != engine.Enqueue || d.Reason != engine.ReasonCapacityExhausted || d.Attempts != i {
			t.Fatalf("attempt %d: %+v %v", i, d, err)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM routing_engine_decisions WHERE conversation_id=$1`, second); n != 1 {
		t.Fatalf("repeating the trigger must update ONE decision row, found %d", n)
	}
	e.exec(t, `UPDATE conversations SET status='closed' WHERE id=$1`, first) // capacity freed
	d, err := e.decide(t, a, second, "")
	if err != nil || d.Kind != engine.AssignAgent || d.AssignedUser != u || d.Attempts != 4 {
		t.Fatalf("after the capacity is freed: %+v %v", d, err)
	}
	if e.count(t, `SELECT count(*) FROM assignment_events WHERE conversation_id=$1`, second) != 1 {
		t.Fatal("one assignment, one history row")
	}
	// a terminal conversation asked again keeps its owner and writes nothing new
	if d, _ := e.decide(t, a, second, ""); d.Reason != engine.ReasonAlreadyAssigned || e.count(t, `SELECT count(*) FROM assignment_events WHERE conversation_id=$1`, second) != 1 {
		t.Fatalf("repeat after assignment: %+v", d)
	}
}

// Concorrência: muitos workers, mais conversas que vagas. Nunca passa da capacidade, nunca atribui duas vezes.
func TestConcurrentWorkersNeverExceedCapacityNorAssignTwice(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "RR", "round_robin")
	var users []uuid.UUID
	for i := 0; i < 4; i++ {
		u, _ := e.attendant(t, a, &q, 3) // 12 slots
		users = append(users, u)
	}
	var convs []uuid.UUID
	for i := 0; i < 20; i++ {
		convs = append(convs, e.conversation(t, a, &q))
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	var mu sync.Mutex
	failures := 0
	for _, c := range convs {
		for dup := 0; dup < 2; dup++ { // every conversation is asked twice at once: a duplicate event
			wg.Add(1)
			go func(c uuid.UUID) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if _, err := e.decide(t, a, c, ""); err != nil {
					mu.Lock()
					failures++
					mu.Unlock()
				}
			}(c)
		}
	}
	wg.Wait()
	if failures > 0 {
		t.Fatalf("%d decisions failed", failures)
	}
	if n := e.count(t, `SELECT count(*) FROM conversations WHERE tenant_id=$1 AND assigned_to_user_id IS NOT NULL`, a); n != 12 {
		t.Fatalf("12 slots must be filled, got %d assigned", n)
	}
	for _, u := range users {
		if n := e.count(t, `SELECT count(*) FROM conversations WHERE tenant_id=$1 AND assigned_to_user_id=$2 AND status='open'`, a, u); n > 3 {
			t.Fatalf("agent %s holds %d conversations, capacity is 3", u, n)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM assignment_events WHERE tenant_id=$1`, a); n != 12 {
		t.Fatalf("one history row per assignment: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM routing_engine_decisions WHERE tenant_id=$1`, a); n != 20 {
		t.Fatalf("one decision row per conversation and trigger: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM routing_engine_decisions WHERE tenant_id=$1 AND decision='ENQUEUE'`, a); n != 8 {
		t.Fatalf("the 8 that did not fit are queued: %d", n)
	}
}

func TestPresenceRequiredAndDownIsRetriedNeverTreatedAsOnline(t *testing.T) {
	pres := &fakePresence{online: map[uuid.UUID]bool{}, fail: true}
	e := newEnv(t, pres)
	a := e.tenant(t)
	q := e.queue(t, a, "RR", "round_robin")
	_, p1 := e.attendant(t, a, &q, 3)
	e.exec(t, `UPDATE tenants SET routing_require_presence=true WHERE id=$1`, a)
	c := e.conversation(t, a, &q)
	if _, err := e.decide(t, a, c, ""); !errors.Is(err, ports.ErrPresenceUnavailable) || e.owner(t, c) != nil {
		t.Fatalf("presence down: %v", err)
	}
	pres.mu.Lock()
	pres.fail = false
	pres.mu.Unlock()
	if d, err := e.decide(t, a, c, ""); err != nil || d.Kind != engine.Enqueue || d.Reason != engine.ReasonNoEligibleAgent {
		t.Fatalf("presence ok but nobody online: %+v %v", d, err)
	}
	pres.mu.Lock()
	pres.online[p1] = true
	pres.mu.Unlock()
	if d, err := e.decide(t, a, c, ""); err != nil || d.Kind != engine.AssignAgent {
		t.Fatalf("online: %+v %v", d, err)
	}
}

// Fora do expediente: espera a abertura com timer durável; o timer é retomado depois de uma queda do worker; agenda alterada replaneja.
func TestClosedSectorWaitsForOpeningWithADurableTimerThatSurvivesWorkerFailureAndScheduleChanges(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "Financeiro", "round_robin")
	u, _ := e.attendant(t, a, &q, 3)
	cal := e.calendar(t, a, "24x7", "24x7", "[]")
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, q, cal)
	// closed now: an emergency suspension from an hour ago to an hour ahead
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at, reason) VALUES ($1,$2,now()-interval '1 hour',now()+interval '1 hour','teste')`, a, cal)
	c := e.conversation(t, a, &q)
	d, err := e.decide(t, a, c, "")
	if err != nil || d.Kind != engine.WaitUntilOpen || d.Reason != engine.ReasonSectorClosed || d.WakeAt == nil || e.owner(t, c) != nil {
		t.Fatalf("closed: %+v %v", d, err)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND kind='open_wakeup' AND state='pending' AND due_at > now()+interval '50 minutes'`, c) != 1 {
		t.Fatal("one durable timer at the opening")
	}
	if n, err := e.eng.RunTimersOnce(e.ctx); err != nil || n != 0 {
		t.Fatalf("nothing is due yet: %d %v", n, err)
	}
	// the schedule changes while the conversation waits: the suspension is lifted -> the timer moves to NOW (same transaction as the change)
	e.exec(t, `DELETE FROM calendar_suspensions WHERE calendar_id=$1`, cal)
	err = platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(c2 context.Context) error {
		_, err := sectorsadapters.ReplanWaiting(c2, platformdb.QuerierFromContext(c2, e.app), a)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var gen int
	_ = e.seed.QueryRow(e.ctx, `SELECT generation FROM routing_engine_timers WHERE conversation_id=$1`, c).Scan(&gen)
	if gen < 2 {
		t.Fatalf("re-planning moves the timer and bumps its generation, got %d", gen)
	}
	// a worker claims the timer and dies before finishing: the lease just sits there
	e.exec(t, `UPDATE routing_engine_timers SET state='leased', lease_until=now()+interval '10 minutes', attempts=1 WHERE conversation_id=$1`, c)
	if n, _ := e.eng.RunTimersOnce(e.ctx); n != 0 {
		t.Fatalf("a live lease must not be taken by another worker: %d", n)
	}
	e.exec(t, `UPDATE routing_engine_timers SET lease_until=now()-interval '1 second' WHERE conversation_id=$1`, c) // the lease expires
	if n, err := e.eng.RunTimersOnce(e.ctx); err != nil || n != 1 {
		t.Fatalf("another worker takes the expired lease: %d %v", n, err)
	}
	if o := e.owner(t, c); o == nil || *o != u {
		t.Fatalf("the waiting conversation must be assigned after the opening: %v", o)
	}
	var tstate string
	var attempts int
	_ = e.seed.QueryRow(e.ctx, `SELECT state, attempts FROM routing_engine_timers WHERE conversation_id=$1`, c).Scan(&tstate, &attempts)
	if tstate != "cancelled" && tstate != "done" {
		t.Fatalf("the timer is finished: %s", tstate)
	}
	if attempts != 2 {
		t.Fatalf("attempts count the claims: %d", attempts)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_decisions WHERE conversation_id=$1 AND trigger='open_wakeup' AND decision='ASSIGN_AGENT'`, c) != 1 {
		t.Fatal("the wake-up decision is recorded")
	}
}

func TestAStaleTimerGenerationNeverCompletesAReplannedTimer(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "S", "round_robin")
	e.attendant(t, a, &q, 3)
	cal := e.calendar(t, a, "c", "24x7", "[]")
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, q, cal)
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at) VALUES ($1,$2,now()-interval '1 hour',now()+interval '2 hours')`, a, cal)
	c := e.conversation(t, a, &q)
	if d, _ := e.decide(t, a, c, ""); d.Kind != engine.WaitUntilOpen {
		t.Fatalf("%+v", d)
	}
	// worker A claims generation 1 ...
	e.exec(t, `UPDATE routing_engine_timers SET state='leased', lease_until=now()+interval '2 minutes', due_at=now() WHERE conversation_id=$1`, c)
	// ... meanwhile the schedule changes and the timer is re-planned (generation 2, pending, due later)
	e.exec(t, `UPDATE calendar_suspensions SET ends_at=now()+interval '5 hours' WHERE calendar_id=$1`, cal)
	if err := platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(c2 context.Context) error {
		_, err := sectorsadapters.ReplanWaiting(c2, platformdb.QuerierFromContext(c2, e.app), a)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var state string
	var gen int
	var due time.Time
	_ = e.seed.QueryRow(e.ctx, `SELECT state, generation, due_at FROM routing_engine_timers WHERE conversation_id=$1`, c).Scan(&state, &gen, &due)
	if state != "pending" || gen != 2 || time.Until(due) < 4*time.Hour {
		t.Fatalf("re-planned: %s gen=%d due in %v", state, gen, time.Until(due))
	}
	// the old claim finishing late cannot touch the new plan
	if n, _ := e.eng.RunTimersOnce(e.ctx); n != 0 {
		t.Fatalf("the re-planned timer is not due: %d", n)
	}
	if e.owner(t, c) != nil {
		t.Fatal("nobody may be assigned while the sector is still closed")
	}
}

func TestNeverOpeningSectorGoesToAPersonAndTimersGiveUpAfterRepeatedFailures(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "S", "round_robin")
	e.attendant(t, a, &q, 3)
	closedForever := e.calendar(t, a, "never", "custom", `[{"day":"mon","intervals":[]}]`)
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, q, closedForever)
	c := e.conversation(t, a, &q)
	d, err := e.decide(t, a, c, "")
	if err != nil || d.Kind != engine.RequireManualAction || d.Reason != engine.ReasonNeverOpens {
		t.Fatalf("never opens: %+v %v", d, err)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_state WHERE conversation_id=$1 AND state='manual_required'`, c) != 1 || e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1`, c) != 0 {
		t.Fatal("manual_required, and no timer that would fire for nothing")
	}
	// a timer that keeps failing: below the limit it only records the error (its lease expires and it is retried); at the limit it is
	// marked failed and the conversation is handed to a person
	c2 := e.conversation(t, a, &q)
	e.exec(t, `INSERT INTO routing_engine_state (tenant_id, conversation_id, state, queue_id) VALUES ($1,$2,'waiting_open',$3) ON CONFLICT (tenant_id, conversation_id) DO UPDATE SET state='waiting_open'`, a, c2, q)
	var timerID uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `INSERT INTO routing_engine_timers (tenant_id, conversation_id, episode, kind, due_at, state, attempts) VALUES ($1,$2,1,'open_wakeup',now(),'leased',3) RETURNING id`, a, c2).Scan(&timerID); err != nil {
		t.Fatal(err)
	}
	token := uuid.New()
	e.exec(t, `UPDATE routing_engine_timers SET claim_token=$2 WHERE id=$1`, timerID, token)
	e.eng.RecordFailureForTest(a, c2, timerID, 1, 1, 3, uuid.New(), errors.New("stale worker")) // a worker with an old token changes nothing
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE id=$1 AND last_error=''`, timerID) != 1 {
		t.Fatal("a stale token must not write")
	}
	e.eng.RecordFailureForTest(a, c2, timerID, 1, 1, 3, token, errors.New("boom"))
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE id=$1 AND state='leased' AND last_error='boom'`, timerID) != 1 || e.count(t, `SELECT count(*) FROM routing_engine_state WHERE conversation_id=$1 AND state='waiting_open'`, c2) != 1 {
		t.Fatal("below the limit the timer keeps its lease (it is retried) and the conversation keeps waiting")
	}
	e.eng.RecordFailureForTest(a, c2, timerID, 1, 1, 8, uuid.New(), errors.New("stale at the limit"))
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE id=$1 AND state='failed'`, timerID) != 0 {
		t.Fatal("a stale worker must not fail the timer nor hand the conversation to a person")
	}
	e.eng.RecordFailureForTest(a, c2, timerID, 1, 1, 8, token, errors.New("boom again"))
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE id=$1 AND state='failed'`, timerID) != 1 || e.count(t, `SELECT count(*) FROM routing_engine_state WHERE conversation_id=$1 AND state='manual_required'`, c2) != 1 {
		t.Fatal("at the limit the timer fails and the conversation is handed to a person")
	}
}

// Cada nova decisão "aguardar abertura" move o MESMO timer; se o horário mudou, a geração sobe (um worker com a geração antiga não conclui).
func TestAskingAgainMovesTheSameTimerAndBumpsItsGenerationOnlyWhenTheOpeningMoved(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "S", "round_robin")
	e.attendant(t, a, &q, 3)
	cal := e.calendar(t, a, "c", "24x7", "[]")
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, q, cal)
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at) VALUES ($1,$2,now()-interval '1 hour',now()+interval '2 hours')`, a, cal)
	c := e.conversation(t, a, &q)
	state := func() (gen int, due time.Time, n int) {
		_ = e.seed.QueryRow(e.ctx, `SELECT generation, due_at FROM routing_engine_timers WHERE conversation_id=$1`, c).Scan(&gen, &due)
		n = e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1`, c)
		return
	}
	e.decide(t, a, c, "")
	g1, d1, n := state()
	e.decide(t, a, c, "") // the sweep asks again: same opening, same plan
	g2, d2, n2 := state()
	if n != 1 || n2 != 1 || g1 != g2 || !d1.Equal(d2) {
		t.Fatalf("asking again with the same opening changes nothing: gen %d->%d, timers %d/%d", g1, g2, n, n2)
	}
	e.exec(t, `UPDATE calendar_suspensions SET ends_at=now()+interval '6 hours' WHERE calendar_id=$1`, cal)
	e.decide(t, a, c, "")
	g3, d3, n3 := state()
	if n3 != 1 || g3 != g1+1 || !d3.After(d1.Add(3*time.Hour)) {
		t.Fatalf("the opening moved: the same timer is re-planned with a new generation: gen %d->%d due %v -> %v timers=%d", g1, g3, d1, d3, n3)
	}
}

// Codex: the chosen candidate is evaluated AGAIN under the locks; a stale snapshot cannot assign to somebody who is no longer eligible.
func TestAStaleCandidateIsRefusedUnderTheLock(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "RR", "round_robin")
	u, _ := e.attendant(t, a, &q, 3)
	c := e.conversation(t, a, &q)
	stale := sectorsadapters.AgentVerdict{UserID: u, Eligible: true, Capacity: 3}
	for name, change := range map[string]string{
		"marked unavailable":     `UPDATE queue_members SET available=false WHERE user_id=$1`,
		"removed from the queue": `UPDATE queue_members SET active=false WHERE user_id=$1`,
		"membership revoked":     `UPDATE memberships SET status='revoked' WHERE user_id=$1`,
		"agent profile disabled": `UPDATE agent_profiles SET status='disabled' WHERE membership_id IN (SELECT id FROM memberships WHERE user_id=$1)`,
	} {
		e.exec(t, `UPDATE queue_members SET active=true, available=true WHERE user_id=$1`, u)
		e.exec(t, `UPDATE memberships SET status='active' WHERE user_id=$1`, u)
		e.exec(t, `UPDATE agent_profiles SET status='active' WHERE membership_id IN (SELECT id FROM memberships WHERE user_id=$1)`, u)
		e.exec(t, change, u)
		ok, err := e.eng.TryAssignForTest(a, q, c, stale)
		if err != nil || ok || e.owner(t, c) != nil {
			t.Fatalf("%s: the snapshot said eligible but the database says no: ok=%v err=%v", name, ok, err)
		}
	}
	// an agent whose shift closed between the snapshot and the write
	e.exec(t, `UPDATE queue_members SET active=true, available=true WHERE user_id=$1`, u)
	e.exec(t, `UPDATE memberships SET status='active' WHERE user_id=$1`, u)
	e.exec(t, `UPDATE agent_profiles SET status='active' WHERE membership_id IN (SELECT id FROM memberships WHERE user_id=$1)`, u)
	closedShift := e.calendar(t, a, "turno fechado", "custom", `[{"day":"mon","intervals":[]}]`)
	e.exec(t, `INSERT INTO agent_shifts (tenant_id, user_id, calendar_id) VALUES ($1,$2,$3)`, a, u, closedShift)
	if ok, err := e.eng.TryAssignForTest(a, q, c, stale); err != nil || ok {
		t.Fatalf("shift closed: %v %v", ok, err)
	}
	e.exec(t, `DELETE FROM agent_shifts WHERE user_id=$1`, u)
	if ok, err := e.eng.TryAssignForTest(a, q, c, stale); err != nil || !ok {
		t.Fatalf("a candidate that is still eligible is assigned: %v %v", ok, err)
	}
}

// Codex: a duplicate trigger after an assignment keeps the original decision of record and only counts the attempt.
func TestTheOriginalAssignmentStaysTheDecisionOfRecord(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "RR", "round_robin")
	u, _ := e.attendant(t, a, &q, 3)
	c := e.conversation(t, a, &q)
	if d, _ := e.decide(t, a, c, ""); d.AssignedUser != u {
		t.Fatalf("%+v", d)
	}
	for i := 0; i < 3; i++ {
		e.decide(t, a, c, "")
	}
	var reason, dest string
	var attempts int
	if err := e.seed.QueryRow(e.ctx, `SELECT reason, destination, attempts FROM routing_engine_decisions WHERE conversation_id=$1`, c).Scan(&reason, &dest, &attempts); err != nil {
		t.Fatal(err)
	}
	if reason != engine.ReasonAssigned || dest != "agent:"+u.String() || attempts != 4 {
		t.Fatalf("decision of record: %s %s attempts=%d", reason, dest, attempts)
	}
}

// Codex: a stale worker (old fencing token) can neither finish, release nor fail a timer that someone else holds.
func TestAStaleFencingTokenCannotTouchANewerClaim(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "S", "round_robin")
	u, _ := e.attendant(t, a, &q, 3)
	c := e.conversation(t, a, &q)
	e.exec(t, `INSERT INTO routing_engine_state (tenant_id, conversation_id, state, queue_id) VALUES ($1,$2,'waiting_open',$3)`, a, c, q)
	fresh, stale := uuid.New(), uuid.New()
	var id uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `INSERT INTO routing_engine_timers (tenant_id, conversation_id, episode, kind, due_at, state, lease_until, attempts, claim_token)
		VALUES ($1,$2,1,'open_wakeup',now(),'leased',now()+interval '1 minute',2,$3) RETURNING id`, a, c, fresh).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := e.eng.RunTimerForTest(a, c, id, 1, 1, 1, stale); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE id=$1 AND state='leased' AND claim_token=$2`, id, fresh) != 1 || e.owner(t, c) != nil {
		t.Fatal("a stale worker must neither complete nor act")
	}
	if err := e.eng.RunTimerForTest(a, c, id, 1, 1, 2, fresh); err != nil {
		t.Fatal(err)
	}
	if o := e.owner(t, c); o == nil || *o != u {
		t.Fatalf("the current holder completes the timer: %v", o)
	}
}

// Codex: one tenant with a huge backlog cannot take a whole batch.
func TestTimerClaimsAreFairAcrossTenants(t *testing.T) {
	e := newEnv(t, nil)
	big, small := e.tenant(t), e.tenant(t)
	for _, tn := range []struct {
		id uuid.UUID
		n  int
	}{{big, 30}, {small, 1}} {
		q := e.queue(t, tn.id, "S", "round_robin")
		for i := 0; i < tn.n; i++ {
			c := e.conversation(t, tn.id, &q)
			e.exec(t, `INSERT INTO routing_engine_state (tenant_id, conversation_id, state, queue_id) VALUES ($1,$2,'waiting_open',$3)`, tn.id, c, q)
			e.exec(t, `INSERT INTO routing_engine_timers (tenant_id, conversation_id, episode, kind, due_at) VALUES ($1,$2,1,'open_wakeup',now()-interval '1 minute')`, tn.id, c)
		}
	}
	// the two tenants' timers are the only ones due in this fresh database run; claim one batch
	if _, err := e.eng.RunTimersOnce(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE tenant_id=$1 AND attempts > 0`, big); n > 10 {
		t.Fatalf("one tenant took %d of a batch, the limit per tenant is 10", n)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE tenant_id=$1 AND attempts > 0`, small) != 1 {
		t.Fatal("the small tenant is served in the same tick")
	}
}

// Codex: conversation -> state -> timer everywhere. Decisions, the timer worker and re-planning hammer the same waiting conversations:
// no deadlock, nothing assigned twice, and what was assigned is never overwritten by a late re-plan.
func TestRoutingTimersAndReplanningDoNotDeadlockAndNeverOverwriteAnAssignment(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "S", "round_robin")
	for i := 0; i < 3; i++ {
		e.attendant(t, a, &q, 20)
	}
	cal := e.calendar(t, a, "c", "24x7", "[]")
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, q, cal)
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at) VALUES ($1,$2,now()-interval '1 hour',now()+interval '2 hours')`, a, cal)
	var convs []uuid.UUID
	for i := 0; i < 12; i++ {
		c := e.conversation(t, a, &q)
		if d, err := e.decide(t, a, c, ""); err != nil || d.Kind != engine.WaitUntilOpen {
			t.Fatalf("%+v %v", d, err)
		}
		convs = append(convs, c)
	}
	e.exec(t, `DELETE FROM calendar_suspensions WHERE calendar_id=$1`, cal) // it opens now
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	run := func(f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 6; i++ {
				if err := f(); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	for _, c := range convs {
		c := c
		run(func() error { _, err := e.decide(t, a, c, ""); return err })
	}
	for i := 0; i < 2; i++ {
		run(func() error {
			return platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(c context.Context) error {
				_, err := sectorsadapters.ReplanWaiting(c, platformdb.QuerierFromContext(c, e.app), a)
				return err
			})
		})
		run(func() error { _, err := e.eng.RunTimersOnce(e.ctx); return err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent routing failed (deadlock?): %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM conversations WHERE tenant_id=$1 AND assigned_to_user_id IS NOT NULL`, a); n != 12 {
		t.Fatalf("all 12 are assigned once the sector opens: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM assignment_events WHERE tenant_id=$1`, a); n != 12 {
		t.Fatalf("one history row each: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM routing_engine_state WHERE tenant_id=$1 AND state<>'assigned'`, a); n != 0 {
		t.Fatalf("a late re-plan must not overwrite an assignment: %d states differ", n)
	}
}

// KNOWN, PRE-EXISTING (not introduced by ADR-0044): the legacy round-robin SQL counts the agent's open conversations with its statement
// snapshot and only then takes the member-row lock (FOR UPDATE ... SKIP LOCKED), so two legacy workers - or a legacy worker and an engine
// worker - that run AT THE SAME INSTANT on the last slot of an agent can both pass the count and overshoot capacity by one. The engine
// itself re-counts with a fresh statement after the lock (TestConcurrentWorkersNeverExceedCapacityNorAssignTwice). This test therefore proves
// what is guaranteed: the two paths share one count and one lock row, so a legacy worker followed by an engine worker (the flag-flip
// window, workers restarted one after the other) never exceeds capacity. Do not run legacy and engine workers concurrently on a queue.
func TestLegacyAndEngineWorkersShareTheLastSlotOfAnAgent(t *testing.T) {
	e := newEnv(t, nil)
	a := e.tenant(t)
	q := e.queue(t, a, "RR", "round_robin")
	u, _ := e.attendant(t, a, &q, 5)
	legacy := routingadapters.NewPostgresAssignmentRepository(e.app, nil)
	var convs []uuid.UUID
	for i := 0; i < 12; i++ {
		convs = append(convs, e.conversation(t, a, &q))
	}
	for i, c := range convs { // alternating, one after the other
		if i%2 == 0 {
			_ = platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(ctx context.Context) error {
				_, _, err := legacy.AssignRoundRobin(ctx, c, "round_robin")
				return err
			})
		} else {
			_, _ = e.decide(t, a, c, "")
		}
	}
	if n := e.count(t, `SELECT count(*) FROM conversations WHERE tenant_id=$1 AND assigned_to_user_id=$2 AND status='open'`, a, u); n != 5 {
		t.Fatalf("capacity is 5: the agent holds %d (legacy and engine must share the count)", n)
	}
}

type recNotifier struct {
	pool *pgxpool.Pool
	mu   sync.Mutex
	sent []struct {
		conv      uuid.UUID
		text, key string
	}
	fail bool
}

func (r *recNotifier) Send(ctx context.Context, conv uuid.UUID, text, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail { // a REAL database error inside the request's transaction (what an unreachable store would cause): it would abort the whole decision without the savepoint
		_, err := platformdb.QuerierFromContext(ctx, r.pool).Exec(ctx, `SELECT 1/0`)
		return err
	}
	r.sent = append(r.sent, struct {
		conv      uuid.UUID
		text, key string
	}{conv, text, key})
	return nil
}

func TestClosedNoticeIsOptInOncePerEpisodeAndNeverBreaksTheDecision(t *testing.T) {
	e := newEnv(t, nil)
	n := &recNotifier{pool: e.app}
	e.eng.WithNotifier(n, t.Logf)
	a := e.tenant(t)
	q := e.queue(t, a, "Financeiro", "round_robin")
	e.attendant(t, a, &q, 3)
	cal := e.calendar(t, a, "24x7", "24x7", "[]")
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, q, cal)
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at, reason) VALUES ($1,$2,now()-interval '1 hour',now()+interval '3 hours','teste')`, a, cal)
	c := e.conversation(t, a, &q)

	// opt-in: no text, no message
	if d, err := e.decide(t, a, c, ""); err != nil || d.Kind != engine.WaitUntilOpen || len(n.sent) != 0 {
		t.Fatalf("without a notice the engine says nothing: %+v %v sent=%d", d, err, len(n.sent))
	}
	e.exec(t, `UPDATE queues SET closed_notice=$2 WHERE id=$1`, q, "Estamos fechados. Voltamos {{proxima_abertura}}.")
	d, err := e.decide(t, a, c, "")
	if err != nil || d.Kind != engine.WaitUntilOpen || len(n.sent) != 1 {
		t.Fatalf("closed with a notice: %+v %v sent=%d", d, err, len(n.sent))
	}
	got := n.sent[0]
	if got.conv != c || got.key == "" || strings.Contains(got.text, "{{") || !strings.HasPrefix(got.text, "Estamos fechados. Voltamos ") || !strings.Contains(got.text, " às ") {
		t.Fatalf("the placeholder must become the opening in local time: %+v", got)
	}
	// asking again in the same episode repeats the SAME key (the sender makes it idempotent)
	if _, err := e.decide(t, a, c, ""); err != nil || len(n.sent) != 2 || n.sent[1].key != got.key {
		t.Fatalf("same episode, same key: %+v %v", n.sent, err)
	}
	// a failing provider changes nothing about the decision and does not abort the transaction
	n.fail = true
	if d, err := e.decide(t, a, c, ""); err != nil || d.Kind != engine.WaitUntilOpen {
		t.Fatalf("a failed notice must not fail routing: %+v %v", d, err)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_state WHERE conversation_id=$1 AND state='waiting_open'`, c) != 1 {
		t.Fatal("the state was still recorded")
	}
	// an OPEN group says nothing even with a notice
	e.exec(t, `DELETE FROM calendar_suspensions WHERE calendar_id=$1`, cal)
	n.fail = false
	before := len(n.sent)
	c2 := e.conversation(t, a, &q)
	if d, err := e.decide(t, a, c2, ""); err != nil || d.Kind == engine.WaitUntilOpen || len(n.sent) != before {
		t.Fatalf("open group, no notice: %+v %v", d, err)
	}
}
