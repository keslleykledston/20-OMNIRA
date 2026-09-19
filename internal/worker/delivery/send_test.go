package delivery_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/worker/delivery"
)

type fakeStore struct {
	job      *delivery.OutboundJob
	ran      uuid.UUID
	sentID   string
	failure  string
	commitOK bool
}

func (s *fakeStore) RunForMessage(ctx context.Context, id uuid.UUID, fn func(context.Context) error) error {
	s.ran = id
	err := fn(ctx)
	s.commitOK = err == nil
	return err
}
func (s *fakeStore) LockOutbound(context.Context, uuid.UUID) (*delivery.OutboundJob, error) {
	return s.job, nil
}
func (s *fakeStore) MarkSent(_ context.Context, _ uuid.UUID, pid string) error {
	s.sentID = pid
	s.job.Status = "sent"
	return nil
}
func (s *fakeStore) MarkFailed(_ context.Context, _ uuid.UUID, reason string) error {
	s.failure = reason
	s.job.Status = "failed"
	return nil
}

type fakeSender struct {
	mu    sync.Mutex
	calls int
	total int // monotonic: provider ids stay unique even if calls is reset
	got   domain.OutboundTextMessage
	conn  uuid.UUID
	err   error
}

func (s *fakeSender) SendText(_ context.Context, conn uuid.UUID, msg domain.OutboundTextMessage) (*domain.SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.total++
	s.got, s.conn = msg, conn
	if s.err != nil {
		return nil, s.err
	}
	return &domain.SendResult{ProviderMessageID: fmt.Sprintf("prov-%s-%d", conn, s.total), State: domain.DeliveryStateSent}, nil
}

func setup(t *testing.T) (*delivery.Handler, *fakeStore, *fakeSender, []byte) {
	msgID, connID := uuid.New(), uuid.New()
	store := &fakeStore{job: &delivery.OutboundJob{MessageID: msgID, ConnectionID: connID, ToE164: "+5511999990000", Text: "oi", Status: "queued", ConnectionActive: true}}
	sender := &fakeSender{}
	h, err := delivery.NewHandler(store, sender, 3)
	if err != nil {
		t.Fatal(err)
	}
	// The envelope carries a forged tenant and payload text: only aggregate_id may matter.
	raw, _ := json.Marshal(map[string]any{"aggregate_id": msgID.String(), "tenant_id": uuid.NewString(), "payload": map[string]string{"text": "forged", "to_e164": "+1"}})
	return h, store, sender, raw
}

func TestDeliversQueuedMessageFromPersistedState(t *testing.T) {
	h, store, sender, raw := setup(t)
	if err := h.Handle(context.Background(), raw, 1); err != nil {
		t.Fatal(err)
	}
	if store.sentID != fmt.Sprintf("prov-%s-1", store.job.ConnectionID) || sender.calls != 1 {
		t.Fatalf("sent=%q calls=%d", store.sentID, sender.calls)
	}
	if sender.got.Text != "oi" || sender.got.ToE164 != "+5511999990000" || sender.got.IdempotencyKey != store.job.MessageID.String() || sender.conn != store.job.ConnectionID {
		t.Fatalf("sender got %+v conn=%s (must use DB state, not envelope)", sender.got, sender.conn)
	}
	if store.ran != store.job.MessageID {
		t.Fatal("tenant session must be derived from the persisted message id")
	}
}

func TestRedeliveryAfterSuccessDoesNotResend(t *testing.T) {
	h, _, sender, raw := setup(t)
	for i := 1; i <= 3; i++ {
		if err := h.Handle(context.Background(), raw, i); err != nil {
			t.Fatal(err)
		}
	}
	if sender.calls != 1 {
		t.Fatalf("provider called %d times for one message", sender.calls)
	}
}

func TestTransientFailureRetriesThenFailsAtLimit(t *testing.T) {
	h, store, sender, raw := setup(t)
	sender.err = fmt.Errorf("boom: %w", ports.ErrProviderUnavailable)
	for attempt := 1; attempt <= 2; attempt++ {
		err := h.Handle(context.Background(), raw, attempt)
		if err == nil || errors.Is(err, delivery.ErrPermanent) {
			t.Fatalf("attempt %d: want retryable error, got %v", attempt, err)
		}
		if store.job.Status != "queued" || store.commitOK {
			t.Fatal("a retry must leave the message queued and roll back")
		}
	}
	if err := h.Handle(context.Background(), raw, 3); err != nil {
		t.Fatalf("final attempt must record failure and ack: %v", err)
	}
	if store.job.Status != "failed" || store.failure != "retries_exhausted:provider_unavailable" {
		t.Fatalf("status=%s reason=%q", store.job.Status, store.failure)
	}
}

func TestPermanentFailureIsRecordedWithoutRetry(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		reason string
	}{
		"auth":       {ports.ErrAuthentication, "authentication"},
		"rejected":   {ports.ErrPermanent, "rejected"},
		"disconnect": {ports.ErrSessionDisconnected, "session_disconnected"},
		"config":     {ports.ErrNotConfigured, "configuration"},
	} {
		t.Run(name, func(t *testing.T) {
			h, store, sender, raw := setup(t)
			sender.err = fmt.Errorf("provider said: secret-ish detail: %w", tc.err)
			if err := h.Handle(context.Background(), raw, 1); err != nil {
				t.Fatalf("permanent failure must ack after recording: %v", err)
			}
			if store.job.Status != "failed" || store.failure != tc.reason {
				t.Fatalf("status=%s reason=%q want %q", store.job.Status, store.failure, tc.reason)
			}
			if sender.calls != 1 {
				t.Fatal("permanent failure must not be retried")
			}
		})
	}
}

func TestInactiveChannelFailsWithoutCallingProvider(t *testing.T) {
	h, store, sender, raw := setup(t)
	store.job.ConnectionActive = false
	if err := h.Handle(context.Background(), raw, 1); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 0 || store.failure != "channel_not_active" {
		t.Fatalf("calls=%d reason=%q", sender.calls, store.failure)
	}
}

func TestMalformedAndUnknownJobsAreTerminal(t *testing.T) {
	h, store, _, _ := setup(t)
	for _, raw := range []string{`not json`, `{}`, `{"aggregate_id":"nope"}`} {
		if err := h.Handle(context.Background(), []byte(raw), 1); !errors.Is(err, delivery.ErrPermanent) {
			t.Fatalf("%s: want ErrPermanent, got %v", raw, err)
		}
	}
	store.job = nil
	unknown, _ := json.Marshal(map[string]string{"aggregate_id": uuid.NewString()})
	if err := h.Handle(context.Background(), unknown, 1); !errors.Is(err, delivery.ErrPermanent) {
		t.Fatalf("unknown message: want ErrPermanent, got %v", err)
	}
}
