package flows_test

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/google/uuid"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	inboxapplication "github.com/omnira/omnira/internal/inbox/application"
)

func (s *stack) lineID() uuid.UUID {
	var id uuid.UUID
	if err := s.env.Seed.QueryRow(context.Background(), `SELECT id FROM channel_connections WHERE tenant_id=$1 LIMIT 1`, s.env.TenantA).Scan(&id); err != nil {
		s.t.Fatal(err)
	}
	return id
}

func newPhone() string { return fmt.Sprintf("+55119%08d", rand.Intn(100000000)) }

// ingestReal runs the REAL inbound service (Postgres stores, optional flow gate) the way the webhook does: one system tenant
// session = one transaction.
func (s *stack) ingestReal(withFlows bool, phone, providerID, text string) (conv uuid.UUID, duplicate bool) {
	s.t.Helper()
	line := s.lineID()
	store := inboxadapters.NewPostgresInboundStore(s.env.App)
	svc := inboxapplication.NewInboundService(store, store, store, inboxadapters.TicketStore{PostgresInboundStore: store}, store)
	if withFlows {
		svc.WithFlows(s.gate)
	}
	s.env.AsSystem(s.t, s.env.TenantA, func(ctx context.Context) {
		res, err := svc.Ingest(ctx, channeldomain.ChannelConnection{ID: line, TenantID: s.env.TenantA},
			channeldomain.InboundMessage{ConnectionID: line.String(), ProviderMessageID: providerID, FromE164: phone, Text: text})
		if err != nil {
			s.t.Fatalf("Ingest: %v", err)
		}
		conv, duplicate = res.Conversation.ID, res.Duplicate
	})
	return
}

func (s *stack) jobsFor(conv uuid.UUID) [][]byte { s.conv = conv; return s.envelopes() }

func TestRealInboundPipelineWithAndWithoutFlows(t *testing.T) {
	s := newStack(t)
	s.publish()
	phone := newPhone()

	// --- flows ON: the first message of a new conversation is held by the flow, not routed by default ---
	conv, dup := s.ingestReal(true, phone, "wamid-1", "oi")
	if dup {
		t.Fatal("first delivery is not a duplicate")
	}
	var mode string
	var queue *uuid.UUID
	_ = s.env.Seed.QueryRow(context.Background(), `SELECT automation_mode, queue_id FROM conversations WHERE id=$1`, conv).Scan(&mode, &queue)
	if mode != "bot" || queue != nil {
		t.Fatalf("a held conversation is not routed to a queue yet: mode=%s queue=%v", mode, queue)
	}
	jobs := s.jobsFor(conv)
	if len(jobs) != 1 {
		t.Fatalf("exactly one flow job for the first message, got %d", len(jobs))
	}
	// The provider redelivers the same webhook: ingest sees a duplicate and enqueues nothing more.
	if _, dup := s.ingestReal(true, phone, "wamid-1", "oi"); !dup || len(s.jobsFor(conv)) != 1 {
		t.Fatal("a redelivered webhook must not enqueue a second job")
	}
	if err := s.handler.Handle(context.Background(), jobs[0]); err != nil {
		t.Fatal(err)
	}
	if s.runStatus() != "waiting_input" || s.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound' AND sent_by_user_id IS NULL`, conv) != 1 {
		t.Fatalf("the bot must greet the new contact: %s", s.runStatus())
	}
	// The contact's answer arrives as an ordinary webhook; the follow-up job finishes the run.
	s.ingestReal(true, phone, "wamid-2", "Carlos")
	jobs = s.jobsFor(conv)
	if len(jobs) != 2 {
		t.Fatalf("a follow-up job expected: %d", len(jobs))
	}
	if err := s.handler.Handle(context.Background(), jobs[1]); err != nil {
		t.Fatal(err)
	}
	_ = s.env.Seed.QueryRow(context.Background(), `SELECT automation_mode, queue_id FROM conversations WHERE id=$1`, conv).Scan(&mode, &queue)
	if s.runStatus() != "completed" || mode != "none" || queue == nil {
		t.Fatalf("a finished bot hands the conversation to the default queue: run=%s mode=%s queue=%v", s.runStatus(), mode, queue)
	}

	// --- flows OFF (no gate, the production default): the legacy pipeline is untouched ---
	other := newPhone()
	conv2, _ := s.ingestReal(false, other, "wamid-9", "oi")
	_ = s.env.Seed.QueryRow(context.Background(), `SELECT automation_mode, queue_id FROM conversations WHERE id=$1`, conv2).Scan(&mode, &queue)
	if mode != "none" || queue == nil {
		t.Fatalf("without the gate the conversation is routed to the default queue exactly as before: mode=%s queue=%v", mode, queue)
	}
	if len(s.jobsFor(conv2)) != 0 || s.count(`SELECT count(*) FROM flow_runs WHERE conversation_id=$1`, conv2) != 0 {
		t.Fatal("flows off: no job and no run")
	}
}
