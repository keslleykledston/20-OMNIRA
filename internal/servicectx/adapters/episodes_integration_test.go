package adapters_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
)

func (s *stack) messagesOf(sess *domain.Session) (out []uuid.UUID) {
	s.t.Helper()
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) {
		cursor := ""
		for {
			refs, next, err := s.svc.Messages(ctx, sess.ID, cursor, 2) // small pages: the cursor is part of what is proved
			if err != nil {
				s.t.Fatal(err)
			}
			for _, r := range refs {
				out = append(out, r.ID)
			}
			if next == "" {
				return
			}
			cursor = next
		}
	})
	return out
}

// Reopening starts a new episode: the messages that arrived while the session was closed are NOT absorbed by it, and the first stretch is untouched.
func TestReopeningDoesNotAbsorbTheTimeTheSessionWasClosed(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	time.Sleep(10 * time.Millisecond)
	m1 := s.message(conv, "inbound", time.Now().UTC(), nil)
	time.Sleep(10 * time.Millisecond)
	closed, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, sess.Version)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	gap := s.message(conv, "inbound", time.Now().UTC(), nil) // arrives while closed
	time.Sleep(10 * time.Millisecond)
	reopened, err := s.reopen(s.svc, s.agent, sess.ID, closed.Version)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	m2 := s.message(conv, "inbound", time.Now().UTC(), nil)
	got := s.messagesOf(reopened)
	if len(got) != 2 || got[0] != m1 || got[1] != m2 {
		t.Fatalf("the reopened session owns its two stretches and not the gap %v: got %v", gap, got)
	}
	if n := s.count(`SELECT count(*) FROM service_session_episodes WHERE session_id=$1`, sess.ID); n != 2 {
		t.Fatalf("two episodes: %d", n)
	}
	if n := s.count(`SELECT count(*) FROM service_session_episodes WHERE session_id=$1 AND ended_at IS NULL`, sess.ID); n != 1 {
		t.Fatalf("exactly one open episode while active: %d", n)
	}
}

// An instant belongs to exactly one side: half-open episodes, and a session starts no earlier than the previous one ended.
func TestAMessageExactlyAtTheBoundaryBelongsToTheLaterSide(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	first := s.mustOpen(s.agent, conv)
	closed, err := s.closeSession(s.agent, first.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	atEnd := s.message(conv, "inbound", *closed.ClosedAt, nil) // exactly at closed_at
	second := s.mustOpen(s.agent, conv)
	atStart := s.message(conv, "inbound", second.OpenedAt, nil) // exactly at the next opened_at
	if got := s.messagesOf(first); len(got) != 0 {
		t.Fatalf("the end of an episode is excluded: %v", got)
	}
	got := s.messagesOf(second)
	if len(got) != 1 || got[0] != atStart {
		t.Fatalf("the start is included: %v (the one at closed_at, %v, is between the two and belongs to neither)", got, atEnd)
	}
}
