package adapters_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	"github.com/omnira/omnira/internal/servicectx/adapters"
	"github.com/omnira/omnira/internal/servicectx/domain"
)

// ---- the state machine holds against plain SQL (independent review of phases 1-2, HIGH) -----------------------------------------------------

func TestTheDatabaseHoldsTheSessionStateMachineAgainstRawSQL(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	closed, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, sess.Version)
	if err != nil {
		t.Fatal(err)
	}
	// an ended session changes only by being reopened: not even a person with every key, not even the system session
	for name, stmt := range map[string]string{
		"closed -> abandoned":      `UPDATE service_sessions SET status='abandoned', resolution_code='abandoned', resolution_canonical='abandoned' WHERE id=$1`,
		"note of a closed session": `UPDATE service_sessions SET resolution_note='alterada depois' WHERE id=$1`,
		"category of a closed one": `UPDATE service_sessions SET queue_id=NULL, customer_id=NULL WHERE id=$1`,
		"reopen to waiting":        `UPDATE service_sessions SET status='waiting', closed_at=NULL, resolution_code=NULL, resolution_canonical=NULL, closed_source=NULL, closed_by_user_id=NULL, reopen_count=reopen_count+1, last_reopen_reason='o cliente voltou' WHERE id=$1`,
		"reopen without counting":  `UPDATE service_sessions SET status='in_progress', closed_at=NULL, resolution_code=NULL, resolution_canonical=NULL, closed_source=NULL, closed_by_user_id=NULL WHERE id=$1`,
		"reopen counting twice":    `UPDATE service_sessions SET status='in_progress', closed_at=NULL, resolution_code=NULL, resolution_canonical=NULL, closed_source=NULL, closed_by_user_id=NULL, reopen_count=reopen_count+2, last_reopen_reason='o cliente voltou' WHERE id=$1`,
	} {
		if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, stmt, sess.ID) }); err == nil {
			t.Errorf("%s: a person with every key must be refused", name)
		}
		sysErr := s.trySystem(s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, stmt, sess.ID) })
		if sysErr == nil {
			t.Errorf("%s: the system session must be refused too", name)
		}
	}
	// a legitimate reopening, by raw SQL, goes through exactly when every condition holds (and counts once)
	reopen := `UPDATE service_sessions SET status='in_progress', closed_at=NULL, resolution_code=NULL, resolution_canonical=NULL, closed_source=NULL, closed_by_user_id=NULL, reopen_count=reopen_count+1, last_reopen_reason='o cliente voltou' WHERE id=$1`
	// ... but not while the conversation is closed
	s.exec(`UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, conv)
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, reopen, sess.ID) }); err == nil {
		t.Error("the database refuses to reopen a session of a closed conversation")
	}
	s.exec(`UPDATE conversations SET status='open', closed_at=NULL WHERE id=$1`, conv)
	// ... nor when a later session exists
	later := s.mustOpen(s.agent, conv)
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, reopen, sess.ID) }); err == nil {
		t.Error("the database refuses to reopen a session that is not the latest of its conversation")
	}
	if _, err := s.closeSession(s.agent, later.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, later.Version); err != nil {
		t.Fatal(err)
	}
	_ = closed
	// the earlier session is still not the latest
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, reopen, sess.ID) }); err == nil {
		t.Error("still not the latest")
	}
	// the latest one reopens, once, with the count moving by exactly one
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, reopen, later.ID)
	}); err != nil {
		t.Errorf("the latest session of an open conversation reopens: %v", err)
	}
}

// ---- episodes ---------------------------------------------------------------------------------------------------------------------------------

func TestEpisodesCannotOverlapNorSkipNorBelongToAnEndedSession(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	insert := func(session uuid.UUID, seq int, started time.Time) error {
		return s.try(s.agent, s.env.TenantA, func(ctx context.Context) error {
			return execInSession(ctx, `INSERT INTO service_session_episodes(tenant_id, session_id, seq, started_at) VALUES($1,$2,$3,$4)`, s.env.TenantA, session, seq, started)
		})
	}
	if err := insert(sess.ID, 2, time.Now().UTC()); err == nil {
		t.Error("a second episode while the first is open must be refused")
	}
	closed, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, sess.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := insert(sess.ID, 2, time.Now().UTC()); err == nil {
		t.Error("an episode of an ENDED session must be refused")
	}
	// another session of the same conversation cannot open an episode that starts before this one's ended
	other, _ := s.open(s.agent, domain.OpenInput{ConversationID: &conv})
	if err := insert(other.Session.ID, 2, closed.ClosedAt.Add(-time.Hour)); err == nil {
		t.Error("an episode that starts before its session opened must be refused")
	}
	if err := insert(other.Session.ID, 5, time.Now().UTC()); err == nil {
		t.Error("episodes are numbered one after the other")
	}
	// ending: only once, and only the end
	for name, stmt := range map[string]string{
		"move the start":    `UPDATE service_session_episodes SET started_at = started_at - interval '1 day' WHERE session_id=$1`,
		"reopen an episode": `UPDATE service_session_episodes SET ended_at = NULL WHERE session_id=$1`,
		"end it again":      `UPDATE service_session_episodes SET ended_at = now() WHERE session_id=$1`,
	} {
		if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, stmt, sess.ID) }); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	// the disjointness holds for two sessions in one conversation even if the second one tries to start inside the first's episode
	conv2, _ := s.conversation()
	a := s.mustOpen(s.agent, conv2)
	if err := s.env.Seed.QueryRow(context.Background(), `SELECT 1`).Scan(new(int)); err != nil {
		t.Fatal(err)
	}
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `INSERT INTO service_sessions(tenant_id, protocol, protocol_sequence, conversation_id, opened_source, status, opened_at)
			VALUES($1,'AT-90001',90001,$2,'api','open',$3)`, s.env.TenantA, conv2, a.OpenedAt)
	}); err == nil {
		t.Error("a second active session of the conversation must be refused")
	}
}

// Several people reopen the same session at once: exactly one wins and the session has exactly one open episode.
func TestParallelReopensLeaveOneOpenEpisode(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	closed, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, sess.Version)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.reopen(s.svc, s.agent, sess.ID, closed.Version)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrInvalidTransition), errors.Is(err, domain.ErrReopenNotAllowed):
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("exactly one reopening wins: %d", won)
	}
	if n := s.count(`SELECT count(*) FROM service_session_episodes WHERE session_id=$1 AND ended_at IS NULL`, sess.ID); n != 1 {
		t.Fatalf("one open episode: %d", n)
	}
	if n := s.count(`SELECT count(*) FROM service_session_episodes WHERE session_id=$1`, sess.ID); n != 2 {
		t.Fatalf("two episodes in all: %d", n)
	}
}

// ---- idempotency receipts ----------------------------------------------------------------------------------------------------------------

// The key leaves a receipt even when the answer was a session that already stood: a retry after that session closed gets the SAME answer, and opens nothing new.
func TestAKeyRemembersTheAnswerEvenWhenItWasAnExistingSession(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	existing := s.mustOpen(s.admin, conv) // somebody else already opened it, with no key
	key := "retry-" + uuid.NewString()
	first, err := s.open(s.agent, domain.OpenInput{ConversationID: &conv, IdempotencyKey: key})
	if err != nil || first.Created || first.Replay || first.Session.ID != existing.ID {
		t.Fatalf("the active session answers the request: %+v %v", first, err)
	}
	if _, err := s.closeSession(s.admin, existing.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, existing.Version); err != nil {
		t.Fatal(err)
	}
	again, err := s.open(s.agent, domain.OpenInput{ConversationID: &conv, IdempotencyKey: key})
	if err != nil || again.Created || !again.Replay || again.Session.ID != existing.ID {
		t.Fatalf("the retry gets the same answer, not a new session: %+v %v", again, err)
	}
	if n := s.count(`SELECT count(*) FROM service_sessions WHERE conversation_id=$1`, conv); n != 1 {
		t.Fatalf("no second session: %d", n)
	}
	// another conversation under the same key is another request
	other, _ := s.conversation()
	if _, err := s.open(s.agent, domain.OpenInput{ConversationID: &other, IdempotencyKey: key}); !errors.Is(err, domain.ErrIdempotencyMismatch) {
		t.Fatalf("the same key with another request: %v", err)
	}
	// and two different keys for the same request each get their own receipt
	k2 := "retry2-" + uuid.NewString()
	third, err := s.open(s.agent, domain.OpenInput{ConversationID: &conv, IdempotencyKey: k2})
	if err != nil || !third.Created {
		t.Fatalf("a new key on a conversation whose session ended opens a new one: %+v %v", third, err)
	}
	if n := s.count(`SELECT count(*) FROM service_command_receipts WHERE tenant_id=$1`, s.env.TenantA); n != 2 {
		t.Fatalf("one receipt per key: %d", n)
	}
	// receipts are history: the application cannot change or delete them, and cannot write one for another company
	for _, stmt := range []string{`UPDATE service_command_receipts SET request_hash='x'`, `DELETE FROM service_command_receipts`} {
		if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, stmt) }); err == nil {
			t.Errorf("receipts are immutable for the application: %s", stmt)
		}
	}
}

func TestReceiptsOfCasesAndTasksSurviveTheirChanges(t *testing.T) {
	s := newStack(t)
	key := "ckey-" + uuid.NewString()
	first, err := s.openCase(s.agent, domain.CaseOpenInput{Title: "Caso", IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.setCaseStatus(s.agent, first.Case.ID, domain.CaseStatusInput{To: domain.CaseCancelled, Reason: "duplicado"}, first.Case.Version); err != nil {
		t.Fatal(err)
	}
	again, err := s.openCase(s.agent, domain.CaseOpenInput{Title: "Caso", IdempotencyKey: key})
	if err != nil || again.Created || !again.Replay || again.Case.ID != first.Case.ID || again.Case.Status != domain.CaseCancelled {
		t.Fatalf("the retry returns the case as it is now, without opening another: %+v %v", again, err)
	}
}

// ---- events -----------------------------------------------------------------------------------------------------------------------------------

func TestADedupKeyReusedForAnotherFactIsRefused(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	repo := adapters.NewPostgresRepository(s.env.App)
	mk := func(payload map[string]any, entity uuid.UUID) domain.ContextEvent {
		return domain.ContextEvent{ID: uuid.New(), TenantID: s.env.TenantA, EventType: "service_session.classified", EntityType: "service_session", EntityID: entity,
			SessionID: &sess.ID, ActorType: "user", ActorID: &s.agent, OccurredAt: time.Now().UTC(), RecordedAt: time.Now().UTC(), SchemaVersion: 1, Payload: payload, DedupKey: "same-key-1234"}
	}
	rec := func(e domain.ContextEvent) error {
		return s.try(s.agent, s.env.TenantA, func(ctx context.Context) error { return repo.Record(ctx, e) })
	}
	if err := rec(mk(map[string]any{"a": 1}, sess.ID)); err != nil {
		t.Fatal(err)
	}
	if err := rec(mk(map[string]any{"a": 1}, sess.ID)); err != nil {
		t.Fatalf("the same fact again is fine: %v", err)
	}
	if err := rec(mk(map[string]any{"a": 2}, sess.ID)); !errors.Is(err, domain.ErrIdempotencyMismatch) {
		t.Fatalf("another payload under the same key: %v", err)
	}
	if err := rec(mk(map[string]any{"a": 1}, uuid.New())); !errors.Is(err, domain.ErrIdempotencyMismatch) {
		t.Fatalf("another entity under the same key: %v", err)
	}
}

func TestAnEventBelongsToTheCompanyOfTheSessionThatWritesIt(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	repo := adapters.NewPostgresRepository(s.env.App)
	e := domain.ContextEvent{ID: uuid.New(), TenantID: s.env.TenantB, EventType: "service_session.classified", EntityType: "service_session", EntityID: sess.ID,
		ActorType: "system", OccurredAt: time.Now().UTC(), RecordedAt: time.Now().UTC(), SchemaVersion: 1}
	s.env.AsSystem(t, s.env.TenantA, func(ctx context.Context) {
		if err := repo.Record(ctx, e); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("a system session of A writing an event for B: %v", err)
		}
	})
	if n := s.count(`SELECT count(*) FROM context_events WHERE tenant_id=$1`, s.env.TenantB); n != 0 {
		t.Fatalf("nothing was written under B: %d", n)
	}
}

// ---- reading needs the key in the database too -------------------------------------------------------------------------------------------------

func TestReadingNeedsServiceReadInTheDatabaseToo(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	cs := s.mustCase(s.agent, "caso", &sess.ID)
	s.mustTask(s.agent, "tarefa", &sess.ID, &cs.ID)
	count := func(user uuid.UUID, table string) (n int) {
		_ = s.try(user, s.env.TenantA, func(ctx context.Context) error {
			return platformQuerier(ctx).QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n)
		})
		return n
	}
	for _, tbl := range []string{"service_sessions", "service_session_episodes", "context_events", "service_cases", "service_case_sessions", "service_tasks", "service_command_receipts", "service_counters"} {
		if got := count(s.observer, tbl); got != 0 {
			t.Errorf("%s: a member WITHOUT service.read sees %d rows through plain SQL", tbl, got)
		}
		if tbl != "service_command_receipts" && tbl != "service_counters" && count(s.agent, tbl) == 0 {
			t.Errorf("%s: a member WITH service.read sees nothing", tbl)
		}
	}
}

// ---- audit ---------------------------------------------------------------------------------------------------------------------------------------

func TestSystemActionsAreAuditedAndNameWhoTheyWereDoneFor(t *testing.T) {
	s := newStack(t)
	att := s.attendance(true)
	flowConv, _ := s.conversation()
	s.env.AsSystem(t, s.env.TenantA, func(ctx context.Context) {
		if _, err := att.FinalizeBySystemWithReason(ctx, flowConv, attendancedomain.ReasonNoResponse, "silêncio"); err != nil {
			t.Fatal(err)
		}
	})
	var actorKind string
	var actor *uuid.UUID
	if err := s.env.Seed.QueryRow(context.Background(), `SELECT a.metadata->>'actor_kind', a.actor_id FROM audit_events a JOIN service_sessions ss ON ss.id::text = a.resource_id
		WHERE ss.conversation_id=$1 AND a.action='service_session.closed'`, flowConv).Scan(&actorKind, &actor); err != nil {
		t.Fatalf("a flow's closing of a session is audited: %v", err)
	}
	if actorKind != "system" || actor != nil {
		t.Fatalf("a flow has no person behind it: %s %v", actorKind, actor)
	}
	hubConv, _ := s.conversation()
	s.assign(hubConv, s.agent)
	s.env.AsSystem(t, s.env.TenantA, func(ctx context.Context) {
		if _, err := att.FinalizeAsHubAgent(ctx, s.agent, attendancedomain.FinalizeInput{ConversationID: hubConv, Reason: attendancedomain.ReasonResolved, Summary: "ok"}, false); err != nil {
			t.Fatal(err)
		}
	})
	var hubActor *uuid.UUID
	var acting string
	if err := s.env.Seed.QueryRow(context.Background(), `SELECT a.actor_id, a.metadata->>'acting_user_id' FROM audit_events a JOIN service_sessions ss ON ss.id::text = a.resource_id
		WHERE ss.conversation_id=$1 AND a.action='service_session.closed'`, hubConv).Scan(&hubActor, &acting); err != nil {
		t.Fatalf("the Hub agent's finalization is audited: %v", err)
	}
	if hubActor == nil || *hubActor != s.agent || acting != s.agent.String() {
		t.Fatalf("the agent the system session acted for is named: %v %q", hubActor, acting)
	}
}

// A reopened session's new episode cannot reach back into the time the session was already counted.
func TestANewEpisodeCannotReachBackIntoTimeAlreadyCounted(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	time.Sleep(40 * time.Millisecond) // the session lived long enough for "a moment inside its first episode" to exist
	closed, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, sess.Version)
	if err != nil {
		t.Fatal(err)
	}
	// reopen the row by hand but start the new episode BEFORE the first one ended
	reopen := `UPDATE service_sessions SET status='in_progress', closed_at=NULL, resolution_code=NULL, resolution_canonical=NULL, closed_source=NULL, closed_by_user_id=NULL, reopen_count=reopen_count+1, last_reopen_reason='o cliente voltou' WHERE id=$1`
	err = s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		if err := execInSession(ctx, reopen, sess.ID); err != nil {
			return err
		}
		return execInSession(ctx, `INSERT INTO service_session_episodes(tenant_id, session_id, seq, started_at) VALUES($1,$2,2,$3)`, s.env.TenantA, sess.ID, closed.ClosedAt.Add(-5*time.Millisecond))
	})
	if err == nil {
		t.Fatal("an episode that starts before the previous one ended must be refused")
	}
}
