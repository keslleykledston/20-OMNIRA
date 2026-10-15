package adapters_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/application"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

func (s *stack) open(user uuid.UUID, in domain.OpenInput) (res application.OpenResult, err error) {
	err = s.try(user, s.env.TenantA, func(ctx context.Context) error {
		var e error
		res, e = s.svc.Open(ctx, in)
		return e
	})
	return res, err
}

func (s *stack) mustOpen(user uuid.UUID, conv uuid.UUID) *domain.Session {
	s.t.Helper()
	res, err := s.open(user, domain.OpenInput{ConversationID: &conv})
	if err != nil || res.Session == nil {
		s.t.Fatalf("open: %v", err)
	}
	return res.Session
}

func (s *stack) get(user, id uuid.UUID) (sess *domain.Session) {
	s.t.Helper()
	s.as(user, s.env.TenantA, func(ctx context.Context) {
		var err error
		if sess, _, err = s.svc.Get(ctx, id); err != nil {
			s.t.Fatal(err)
		}
	})
	return sess
}

func (s *stack) closeSession(user, id uuid.UUID, in domain.CloseInput, version int) (sess *domain.Session, err error) {
	err = s.try(user, s.env.TenantA, func(ctx context.Context) error {
		var e error
		sess, e = s.svc.Close(ctx, id, in, version)
		return e
	})
	return sess, err
}

func (s *stack) reopen(svc *application.Service, user, id uuid.UUID, version int) (sess *domain.Session, err error) {
	err = s.try(user, s.env.TenantA, func(ctx context.Context) error {
		var e error
		sess, e = svc.Reopen(ctx, id, domain.ReopenInput{Reason: "o cliente voltou a falar"}, version)
		return e
	})
	return sess, err
}

// ---- Gate 1: a complete informational attendance -------------------------------------------------------------------------------------

func TestInformationalAttendanceHasProtocolMessagesClassificationAndResult(t *testing.T) {
	s := newStack(t)
	conv, contact := s.conversation()
	before := time.Now().UTC().Add(-3 * time.Minute)
	earlier := s.message(conv, "inbound", before, nil) // arrived BEFORE the attendance started: not classified yet

	sess := s.mustOpen(s.agent, conv)
	if sess.Protocol != "AT-10000" || sess.ProtocolSequence != 10000 || sess.Status != domain.SessionOpen || sess.Version != 1 {
		t.Fatalf("first session of the company: %+v", sess)
	}
	if sess.ContactID == nil || *sess.ContactID != contact || sess.ConversationID == nil || *sess.ConversationID != conv {
		t.Fatalf("the contact comes from the conversation: %+v", sess)
	}
	if sess.OpenedBy == nil || *sess.OpenedBy != s.agent || sess.OpenedSource != domain.SourceOperator {
		t.Fatalf("who opened: %+v", sess)
	}
	// the conversation goes on: a customer message and a person's answer, then classification and the result
	time.Sleep(20 * time.Millisecond)
	in1 := s.message(conv, "inbound", time.Now().UTC(), nil)
	time.Sleep(20 * time.Millisecond)
	out1 := s.message(conv, "outbound", time.Now().UTC(), &s.agent)
	var cat uuid.UUID
	s.as(s.admin, s.env.TenantA, func(ctx context.Context) {
		c, err := s.catalog.CreateCategory(ctx, "internet", "Internet")
		if err != nil {
			t.Fatal(err)
		}
		cat = c.ID
	})
	var patched *domain.Session
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) (e error) {
		patched, e = s.svc.Patch(ctx, sess.ID, domain.PatchInput{CategoryID: &cat}, sess.Version)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if patched.CategoryID == nil || *patched.CategoryID != cat || patched.Version != 2 {
		t.Fatalf("classified: %+v", patched)
	}
	var moved *domain.Session
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) (e error) {
		moved, e = s.svc.SetStatus(ctx, sess.ID, domain.SessionInProgress, patched.Version)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	closed, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResInformationProvided, Note: "cliente só queria o horário"}, moved.Version)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != domain.SessionClosed || closed.ClosedAt == nil || closed.ClosedBy == nil || *closed.ClosedBy != s.agent ||
		closed.ResolutionCanonical == nil || *closed.ResolutionCanonical != domain.ResInformationProvided {
		t.Fatalf("closed: %+v", closed)
	}
	if closed.FirstResponseAt == nil || closed.FirstResponseAt.Before(sess.OpenedAt) {
		t.Fatalf("first response is the person's first answer inside the session: %v", closed.FirstResponseAt)
	}
	// messages belong to the session by INTERVAL, by reference: the early one is not in it, nothing was copied
	var refs []ports.MessageRef
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) {
		var e error
		if refs, _, e = s.svc.Messages(ctx, sess.ID, "", 50); e != nil {
			t.Fatal(e)
		}
	})
	got := []uuid.UUID{}
	for _, r := range refs {
		got = append(got, r.ID)
	}
	if len(got) != 2 || got[0] != in1 || got[1] != out1 {
		t.Fatalf("session messages = %v, want [%v %v] and not the early %v", got, in1, out1, earlier)
	}
	if n := s.count(`SELECT count(*) FROM messages WHERE conversation_id=$1`, conv); n != 3 {
		t.Fatalf("opening, classifying and closing a session never copies or removes a message: %d", n)
	}
	// no service order, no case, no task were needed
	if n := s.count(`SELECT count(*) FROM tickets WHERE conversation_id=$1 AND external_ticket_id IS NOT NULL`, conv); n != 0 {
		t.Fatal("an informational attendance creates no external record")
	}
	// the audit trail names the actions with ids and enums only
	var meta string
	if err := s.env.Seed.QueryRow(context.Background(), `SELECT string_agg(metadata::text, ' ') FROM audit_events WHERE tenant_id=$1 AND resource_id=$2`, s.env.TenantA, sess.ID).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(meta, "information_provided") || strings.Contains(meta, "horário") {
		t.Fatalf("audit has results and ids, never the note: %s", meta)
	}
}

// ---- protocol ----------------------------------------------------------------------------------------------------------------------------

func TestProtocolIsUniqueAndDenseUnderConcurrencyAndPerCompany(t *testing.T) {
	s := newStack(t)
	const n = 24
	convs := make([]uuid.UUID, n)
	for i := range convs {
		convs[i], _ = s.conversation()
	}
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(c uuid.UUID) {
			defer wg.Done()
			_, err := s.open(s.agent, domain.OpenInput{ConversationID: &c})
			errs <- err
		}(convs[i])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent open: %v", err)
		}
	}
	rows, err := s.env.Seed.Query(context.Background(), `SELECT protocol_sequence FROM service_sessions WHERE tenant_id=$1 ORDER BY protocol_sequence`, s.env.TenantA)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var seqs []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, v)
	}
	if len(seqs) != n {
		t.Fatalf("%d sessions, want %d", len(seqs), n)
	}
	for i, v := range seqs {
		if v != int64(10000+i) {
			t.Fatalf("protocols must be dense and unique, got %v", seqs)
		}
	}
	// another company numbers independently, from the same start
	connB, _ := s.env.SeedConversation(t, s.env.TenantB, "none")
	var b *domain.Session
	if err := s.try(s.env.UserB, s.env.TenantB, func(ctx context.Context) (e error) {
		res, e := s.svc.Open(ctx, domain.OpenInput{ConversationID: &connB})
		b = res.Session
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if b.Protocol != "AT-10000" {
		t.Fatalf("protocols are unique per company, not global: %s", b.Protocol)
	}
}

func TestARolledBackOpenGivesTheNumberBack(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	bad := uuid.New() // a queue that does not exist: Open validates BEFORE taking the number, but even a failure after it must not burn it
	_, err := s.open(s.agent, domain.OpenInput{ConversationID: &conv, QueueID: &bad})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown queue: %v", err)
	}
	if sess := s.mustOpen(s.agent, conv); sess.Protocol != "AT-10000" {
		t.Fatalf("a failed open leaves no hole: %s", sess.Protocol)
	}
}

// ---- idempotency ---------------------------------------------------------------------------------------------------------------------------

func TestIdempotentOpenReplaysAndRefusesAnotherRequest(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	key := "open-" + uuid.NewString()
	first, err := s.open(s.agent, domain.OpenInput{ConversationID: &conv, IdempotencyKey: key})
	if err != nil || !first.Created {
		t.Fatalf("first: %+v %v", first, err)
	}
	again, err := s.open(s.agent, domain.OpenInput{ConversationID: &conv, IdempotencyKey: key})
	if err != nil || again.Created || again.Session.ID != first.Session.ID {
		t.Fatalf("replay must return the same session: %+v %v", again, err)
	}
	other, _ := s.conversation()
	if _, err := s.open(s.agent, domain.OpenInput{ConversationID: &other, IdempotencyKey: key}); !errors.Is(err, domain.ErrIdempotencyMismatch) {
		t.Fatalf("the same key with another request: %v", err)
	}
	if n := s.count(`SELECT count(*) FROM service_sessions WHERE tenant_id=$1`, s.env.TenantA); n != 1 {
		t.Fatalf("one session: %d", n)
	}
}

func TestSameKeyInParallelCreatesOneSession(t *testing.T) {
	s := newStack(t)
	key := "par-" + uuid.NewString()
	const n = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, ids := 0, map[uuid.UUID]bool{}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := s.open(s.agent, domain.OpenInput{IdempotencyKey: key, OriginChannel: "phone"}) // no conversation: an attendance by phone, logged through the API
			if err != nil {
				t.Errorf("open: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if res.Created {
				created++
			}
			ids[res.Session.ID] = true
		}()
	}
	wg.Wait()
	if created != 1 || len(ids) != 1 {
		t.Fatalf("one creation and one session expected, got created=%d ids=%d", created, len(ids))
	}
}

// ---- many sessions in one conversation, transfers, reopening --------------------------------------------------------------------------------

func TestManySessionsInOneConversationEachWithItsOwnMessagesAndProtocol(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	first := s.mustOpen(s.agent, conv)
	if again := s.mustOpen(s.agent, conv); again.ID != first.ID {
		t.Fatal("opening again returns the ACTIVE session: at most one at a time")
	}
	time.Sleep(15 * time.Millisecond)
	m1 := s.message(conv, "inbound", time.Now().UTC(), nil)
	time.Sleep(15 * time.Millisecond)
	closed, err := s.closeSession(s.agent, first.ID, domain.CloseInput{ResolutionCode: domain.ResInformationProvided}, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(15 * time.Millisecond)
	gap := s.message(conv, "inbound", time.Now().UTC(), nil) // between the two sessions: unclassified
	time.Sleep(15 * time.Millisecond)
	second := s.mustOpen(s.agent, conv)
	if second.ID == first.ID || second.Protocol != "AT-10001" {
		t.Fatalf("a new session of the same conversation has its own protocol: %+v", second)
	}
	if second.OpenedAt.Before(*closed.ClosedAt) {
		t.Fatal("intervals of one conversation stay disjoint")
	}
	time.Sleep(15 * time.Millisecond)
	m2 := s.message(conv, "inbound", time.Now().UTC(), nil)
	list := func(sess *domain.Session) []uuid.UUID {
		var out []uuid.UUID
		s.as(s.agent, s.env.TenantA, func(ctx context.Context) {
			refs, _, err := s.svc.Messages(ctx, sess.ID, "", 50)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range refs {
				out = append(out, r.ID)
			}
		})
		return out
	}
	if got := list(first); len(got) != 1 || got[0] != m1 {
		t.Fatalf("first session messages %v", got)
	}
	if got := list(second); len(got) != 1 || got[0] != m2 {
		t.Fatalf("second session messages %v (the one between sessions, %v, belongs to none)", got, gap)
	}
	if n := s.count(`SELECT count(*) FROM service_sessions WHERE conversation_id=$1`, conv); n != 2 {
		t.Fatalf("two sessions in the conversation: %d", n)
	}
}

func TestATransferOfTheConversationCreatesNoSessionAndKeepsTheProtocol(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	s.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, conv, s.agent)
	s.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, conv, s.admin) // moved to somebody else
	if n := s.count(`SELECT count(*) FROM service_sessions WHERE conversation_id=$1`, conv); n != 1 {
		t.Fatalf("a transfer is not a new attendance: %d sessions", n)
	}
	var assigned *uuid.UUID
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) {
		var err error
		if _, assigned, err = s.svc.Get(ctx, sess.ID); err != nil {
			t.Fatal(err)
		}
	})
	if assigned == nil || *assigned != s.admin {
		t.Fatalf("the responsible person is the conversation's, read live: %v", assigned)
	}
}

func TestReopeningHasItsRules(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	closed, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, sess.Version)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := s.reopen(s.svc, s.agent, sess.ID, closed.Version)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status != domain.SessionInProgress || reopened.ReopenCount != 1 || reopened.Protocol != sess.Protocol || reopened.ClosedAt != nil || reopened.ResolutionCode != nil {
		t.Fatalf("reopened keeps the protocol and drops the result: %+v", reopened)
	}
	// not active-session-free: a reopened one cannot be reopened again, and another one cannot be opened beside it
	if _, err := s.reopen(s.svc, s.agent, sess.ID, reopened.Version); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("reopening an active session: %v", err)
	}
	closed2, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, reopened.Version)
	if err != nil {
		t.Fatal(err)
	}
	// a later session exists: the earlier one is no longer reopenable
	next := s.mustOpen(s.agent, conv)
	if _, err := s.closeSession(s.agent, next.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, next.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.reopen(s.svc, s.agent, sess.ID, closed2.Version); !errors.Is(err, domain.ErrReopenNotAllowed) {
		t.Fatalf("only the latest session reopens: %v", err)
	}
	// a closed conversation is final (ADR-0020)
	s.exec(`UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, conv)
	last, _ := s.get(s.agent, next.ID), 0
	if _, err := s.reopen(s.svc, s.agent, next.ID, last.Version); !errors.Is(err, domain.ErrReopenNotAllowed) {
		t.Fatalf("a closed conversation cannot have its session reopened: %v", err)
	}
}

func TestReopenWindow(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	closed, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, sess.Version)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	tight, _, _ := newTight(s)
	if _, err := s.reopen(tight, s.agent, sess.ID, closed.Version); !errors.Is(err, domain.ErrReopenNotAllowed) {
		t.Fatalf("past the window: %v", err)
	}
}

// ---- state machine, results, versions ------------------------------------------------------------------------------------------------------

func TestStateMachine(t *testing.T) {
	allowed := map[[2]domain.SessionStatus]bool{
		{domain.SessionOpen, domain.SessionInProgress}: true, {domain.SessionOpen, domain.SessionWaiting}: true, {domain.SessionOpen, domain.SessionClosed}: true,
		{domain.SessionOpen, domain.SessionAbandoned}: true, {domain.SessionInProgress, domain.SessionWaiting}: true, {domain.SessionInProgress, domain.SessionClosed}: true,
		{domain.SessionInProgress, domain.SessionAbandoned}: true, {domain.SessionWaiting, domain.SessionInProgress}: true, {domain.SessionWaiting, domain.SessionClosed}: true,
		{domain.SessionWaiting, domain.SessionAbandoned}: true, {domain.SessionClosed, domain.SessionInProgress}: true, {domain.SessionAbandoned, domain.SessionInProgress}: true,
	}
	all := []domain.SessionStatus{domain.SessionOpen, domain.SessionInProgress, domain.SessionWaiting, domain.SessionClosed, domain.SessionAbandoned}
	for _, from := range all {
		for _, to := range all {
			if got := domain.CanTransition(from, to); got != allowed[[2]domain.SessionStatus{from, to}] {
				t.Errorf("%s -> %s = %v", from, to, got)
			}
		}
	}
}

func TestClosingNeedsAKnownResultAndAbandonedHasItsOwn(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	if _, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{}, sess.Version); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a result is required: %v", err)
	}
	if _, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: "nao_existe"}, sess.Version); !errors.Is(err, domain.ErrUnknownResolution) {
		t.Fatalf("unknown company code: %v", err)
	}
	if _, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResAbandoned}, sess.Version); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("'abandoned' is only for an abandoned session: %v", err)
	}
	// a company code that POINTS to a canonical one
	s.as(s.admin, s.env.TenantA, func(ctx context.Context) {
		if _, err := s.catalog.CreateResultCode(ctx, "segunda_via", domain.ResInformationProvided, "Segunda via enviada"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.catalog.CreateResultCode(ctx, domain.ResOther, domain.ResOther, "x"); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("a company code cannot take a built-in name: %v", err)
		}
	})
	closed, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: "segunda_via"}, sess.Version)
	if err != nil {
		t.Fatal(err)
	}
	if *closed.ResolutionCode != "segunda_via" || *closed.ResolutionCanonical != domain.ResInformationProvided {
		t.Fatalf("reports aggregate by the canonical code: %+v", closed)
	}
	// abandoning
	conv2, _ := s.conversation()
	other := s.mustOpen(s.agent, conv2)
	ab, err := s.closeSession(s.agent, other.ID, domain.CloseInput{Abandon: true}, other.Version)
	if err != nil || ab.Status != domain.SessionAbandoned || *ab.ResolutionCanonical != domain.ResAbandoned {
		t.Fatalf("abandon: %+v %v", ab, err)
	}
	// closed is final for edits and for status moves
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) error {
		_, e := s.svc.SetStatus(ctx, sess.ID, domain.SessionWaiting, closed.Version)
		return e
	}); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("closed session: %v", err)
	}
}

func TestOptimisticVersionIsRequiredAndAdvancedByTheDatabase(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	if _, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, 0); !errors.Is(err, domain.ErrPreconditionNeeded) {
		t.Fatalf("no version: %v", err)
	}
	if _, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, sess.Version+5); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale version: %v", err)
	}
	// two people close from the same read: exactly one wins
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, u := range []uuid.UUID{s.agent, s.admin} {
		wg.Add(1)
		go func(u uuid.UUID) {
			defer wg.Done()
			_, err := s.closeSession(u, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, sess.Version)
			results <- err
		}(u)
	}
	wg.Wait()
	close(results)
	ok, lost := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrInvalidTransition):
			lost++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || lost != 1 {
		t.Fatalf("exactly one closer wins, got ok=%d lost=%d", ok, lost)
	}
	// the database advances the version itself, even for a writer that forgets
	var v int
	s.env.AsSystem(t, s.env.TenantA, func(ctx context.Context) {
		if err := execInSession(ctx, `UPDATE service_sessions SET resolution_note='x' WHERE id=$1`, sess.ID); err != nil {
			t.Fatal(err)
		}
	})
	if err := s.env.Seed.QueryRow(context.Background(), `SELECT version FROM service_sessions WHERE id=$1`, sess.ID).Scan(&v); err != nil || v < 3 {
		t.Fatalf("version after a raw update: %d %v", v, err)
	}
}

// ---- security: tenant isolation, keys, database guards -------------------------------------------------------------------------------------

func TestAnotherCompanyNeverSeesNorTouchesASession(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	err := s.try(s.env.UserB, s.env.TenantB, func(ctx context.Context) error {
		_, _, e := s.svc.Get(ctx, sess.ID)
		return e
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("tenant B reading A's session: %v", err)
	}
	if err := s.try(s.env.UserB, s.env.TenantB, func(ctx context.Context) error {
		_, e := s.svc.Open(ctx, domain.OpenInput{ConversationID: &conv}) // A's conversation id used inside B
		return e
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("tenant B opening on A's conversation: %v", err)
	}
	var items []domain.Session
	if err := s.try(s.env.UserB, s.env.TenantB, func(ctx context.Context) (e error) {
		items, _, e = s.svc.List(ctx, ports.SessionFilter{})
		return e
	}); err != nil || len(items) != 0 {
		t.Fatalf("tenant B lists nothing of A: %v %v", items, err)
	}
	// B's person asking A's tenant through the path: not a member there
	if err := s.try(s.env.UserB, s.env.TenantA, func(ctx context.Context) error {
		_, _, e := s.svc.Get(ctx, sess.ID)
		return e
	}); err == nil {
		t.Fatal("a person with no membership in A must not read A's session")
	}
	// customers, queues and categories of another company are not references one can use
	custB := uuid.New()
	s.exec(`INSERT INTO customer_accounts(id, tenant_id, name) VALUES($1,$2,'Empresa B')`, custB, s.env.TenantB)
	conv2, _ := s.conversation()
	if _, err := s.open(s.agent, domain.OpenInput{ConversationID: &conv2, CustomerID: &custB}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a customer of another company: %v", err)
	}
}

func TestKeysAreAskedByTheServiceAndByTheDatabase(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	if _, err := s.open(s.observer, domain.OpenInput{ConversationID: &conv}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("no service.create: %v", err)
	}
	sess := s.mustOpen(s.limited, conv) // may read, open and edit
	if _, err := s.closeSession(s.limited, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, sess.Version); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("no service.close: %v", err)
	}
	// the DATABASE says the same when the application is bypassed: raw SQL as the same person
	rawClose := func(user uuid.UUID) error {
		return s.try(user, s.env.TenantA, func(ctx context.Context) error {
			return execInSession(ctx, `UPDATE service_sessions SET status='closed', closed_at=now(), resolution_code='other', resolution_canonical='other' WHERE id=$1`, sess.ID)
		})
	}
	err := rawClose(s.limited)
	if err == nil || !strings.Contains(err.Error(), "service.close") {
		t.Fatalf("the row guard asks service.close of whoever closes: %v", err)
	}
	if err := rawClose(s.agent); err != nil {
		t.Fatalf("a person with the key closes: %v", err)
	}
	// and a plain INSERT without service.create is refused by the policy
	err = s.try(s.observer, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `INSERT INTO service_sessions(tenant_id, protocol, protocol_sequence, opened_source) VALUES($1,'AT-1',1,'api')`, s.env.TenantA)
	})
	if err == nil {
		t.Fatal("RLS must refuse an insert without service.create")
	}
}

func TestDatabaseGuards(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	ex := func(sql string, args ...any) error {
		_, err := s.env.Seed.Exec(context.Background(), sql, args...)
		return err
	}
	if err := ex(`UPDATE service_sessions SET protocol='AT-1' WHERE id=$1`, sess.ID); err == nil {
		t.Error("the protocol never changes")
	}
	if err := ex(`UPDATE service_sessions SET tenant_id=$2 WHERE id=$1`, sess.ID, s.env.TenantB); err == nil {
		t.Error("a session never changes company")
	}
	if err := ex(`UPDATE service_sessions SET conversation_id=NULL WHERE id=$1`, sess.ID); err == nil {
		t.Error("a session never leaves its conversation")
	}
	if err := ex(`INSERT INTO service_sessions(tenant_id, protocol, protocol_sequence, conversation_id, opened_source) VALUES($1,'AT-99999',99999,$2,'api')`, s.env.TenantA, conv); err == nil {
		t.Error("a second ACTIVE session of the conversation must be refused")
	}
	if err := ex(`INSERT INTO service_sessions(tenant_id, protocol, protocol_sequence, opened_source) VALUES($1,'not a protocol',99998,'api')`, s.env.TenantA); err == nil {
		t.Error("the protocol has a shape")
	}
	if err := ex(`UPDATE service_sessions SET status='closed' WHERE id=$1`, sess.ID); err == nil {
		t.Error("a closed session needs a time and a result")
	}
	// disjoint intervals: closing, then inserting a session that starts BEFORE the end of the first
	closed, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, sess.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := ex(`INSERT INTO service_sessions(tenant_id, protocol, protocol_sequence, conversation_id, opened_source, opened_at) VALUES($1,'AT-99997',99997,$2,'api',$3)`,
		s.env.TenantA, conv, closed.ClosedAt.Add(-time.Minute)); err == nil {
		t.Error("a session that starts before the previous one ended must be refused")
	}
	// nothing is ever deleted by the application role
	err = s.try(s.agent, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `DELETE FROM service_sessions WHERE id=$1`, sess.ID)
	})
	if err == nil {
		t.Error("the application role must not delete sessions")
	}
}

func TestListPaginatesByKeysetWithFilters(t *testing.T) {
	s := newStack(t)
	var ids []uuid.UUID
	for i := 0; i < 5; i++ {
		c, _ := s.conversation()
		ids = append(ids, s.mustOpen(s.agent, c).ID)
		time.Sleep(5 * time.Millisecond)
	}
	var seen []uuid.UUID
	cursor := ""
	for page := 0; page < 10; page++ {
		var items []domain.Session
		var next string
		s.as(s.agent, s.env.TenantA, func(ctx context.Context) {
			var err error
			if items, next, err = s.svc.List(ctx, ports.SessionFilter{Limit: 2, Cursor: cursor}); err != nil {
				t.Fatal(err)
			}
		})
		for _, it := range items {
			seen = append(seen, it.ID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	got := append([]uuid.UUID(nil), seen...)
	sort.Slice(got, func(i, j int) bool { return got[i].String() < got[j].String() })
	if fmt.Sprint(got) != fmt.Sprint(ids) || len(seen) != 5 {
		t.Fatalf("every session exactly once across pages: %v vs %v", seen, ids)
	}
	var byProtocol []domain.Session
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) {
		byProtocol, _, _ = s.svc.List(ctx, ports.SessionFilter{Protocol: "at-10002"})
	})
	if len(byProtocol) != 1 || byProtocol[0].Protocol != "AT-10002" {
		t.Fatalf("protocol filter: %v", byProtocol)
	}
}

// Many people open an attendance on the same conversation at once: one session is created and every caller sees it (the conversation lock serializes them).
func TestParallelOpensOnOneConversationCreateOneSession(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	const n = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, ids := 0, map[uuid.UUID]bool{}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := s.open(s.agent, domain.OpenInput{ConversationID: &conv})
			if err != nil {
				t.Errorf("open: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if res.Created {
				created++
			}
			ids[res.Session.ID] = true
		}()
	}
	wg.Wait()
	if created != 1 || len(ids) != 1 {
		t.Fatalf("one session for the conversation, got created=%d ids=%d", created, len(ids))
	}
}

// A system session (the worker, the flow engine) bypasses RLS, so the tenant predicate in the SQL is the only barrier there: it must hold on its own.
func TestASystemSessionOfAnotherCompanyStillCannotReachASession(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	s.env.AsSystem(t, s.env.TenantB, func(ctx context.Context) {
		if _, _, err := s.svc.Get(ctx, sess.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("get from a system session of B: %v", err)
		}
		if _, _, err := s.svc.ForConversation(ctx, conv); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("by conversation from a system session of B: %v", err)
		}
		if err := s.svc.CloseForFinalize(ctx, conv, "resolved", "flow", uuid.Nil); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("finalize hook from a system session of B: %v", err)
		}
	})
}
