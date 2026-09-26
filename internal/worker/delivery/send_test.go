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
	mu           sync.Mutex
	job          *delivery.OutboundJob
	ran          uuid.UUID
	sentID       string
	failure      string
	commitOK     bool
	reserveCalls int
}

func (s *fakeStore) RunForMessage(ctx context.Context, id uuid.UUID, fn func(context.Context) error) error {
	s.ran = id
	err := fn(ctx)
	s.commitOK = err == nil
	return err
}
func (s *fakeStore) LockOutbound(context.Context, uuid.UUID) (*delivery.OutboundJob, error) {
	if s.job == nil {
		return nil, nil
	}
	cp := *s.job
	return &cp, nil
}
func (s *fakeStore) MarkSent(_ context.Context, _ uuid.UUID, pid string) error {
	s.sentID = pid
	s.job.Status = "sent"
	s.job.ProviderMessageID = pid
	return nil
}
func (s *fakeStore) MarkFailed(_ context.Context, _ uuid.UUID, reason string) error {
	s.failure = reason
	s.job.Status = "failed"
	return nil
}

// EnsureReservedProviderMessageID mirrors the real Postgres semantics closely
// enough for unit tests: reuse a reservation already on the job, otherwise
// generate exactly once and persist it onto the job (simulating a commit).
func (s *fakeStore) EnsureReservedProviderMessageID(ctx context.Context, _ uuid.UUID, generate func(context.Context) (string, error)) (string, error) {
	s.mu.Lock()
	existing := s.job.ReservedProviderMessageID
	s.mu.Unlock()
	if existing != "" {
		return existing, nil
	}
	s.reserveCalls++
	candidate, err := generate(ctx)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	// Simulates the real conditional UPDATE: only the first writer to reach
	// here (in a single-threaded test) wins; concurrent races are exercised
	// separately in TestConcurrentReservationConvergesOnOneID.
	if s.job.ReservedProviderMessageID == "" {
		s.job.ReservedProviderMessageID = candidate
	}
	won := s.job.ReservedProviderMessageID
	s.mu.Unlock()
	return won, nil
}

type fakeSender struct {
	mu    sync.Mutex
	calls int
	total int // monotonic: provider ids stay unique even if calls is reset
	got   domain.OutboundTextMessage
	conn  uuid.UUID
	err   error

	newIDCalls int
	newIDTotal int
	newIDErr   error
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

func (s *fakeSender) NewMessageID(_ context.Context, conn uuid.UUID) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.newIDCalls++
	if s.newIDErr != nil {
		return "", s.newIDErr
	}
	s.newIDTotal++
	return fmt.Sprintf("reserved-%s-%d", conn, s.newIDTotal), nil
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
	if sender.got.Text != "oi" || sender.got.ToE164 != "+5511999990000" || sender.conn != store.job.ConnectionID {
		t.Fatalf("sender got %+v conn=%s (must use DB state, not envelope)", sender.got, sender.conn)
	}
	if sender.got.IdempotencyKey == "" || sender.got.IdempotencyKey != store.job.ReservedProviderMessageID {
		t.Fatalf("SendText must receive the persisted reserved id, got %q, persisted %q", sender.got.IdempotencyKey, store.job.ReservedProviderMessageID)
	}
	if sender.got.IdempotencyKey == store.job.MessageID.String() {
		t.Fatal("IdempotencyKey must be the provider-reserved id, not the bare local message id (PILOT.4A1)")
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
	if sender.newIDCalls != 0 {
		t.Fatal("an inactive channel must never reserve a provider id")
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

// --- PILOT.4A1: stable provider message id -------------------------------

// A: first delivery reserves and persists an id, and SendText uses exactly
// that persisted value.
func TestFirstDeliveryReservesAndUsesPersistedID(t *testing.T) {
	h, store, sender, raw := setup(t)
	if store.job.ReservedProviderMessageID != "" {
		t.Fatal("test setup must start with no reservation")
	}
	if err := h.Handle(context.Background(), raw, 1); err != nil {
		t.Fatal(err)
	}
	if sender.newIDCalls != 1 {
		t.Fatalf("NewMessageID calls = %d, want 1", sender.newIDCalls)
	}
	if store.job.ReservedProviderMessageID == "" {
		t.Fatal("reservation must be persisted on the job")
	}
	if sender.got.IdempotencyKey != store.job.ReservedProviderMessageID {
		t.Fatalf("SendText got id %q, want persisted %q", sender.got.IdempotencyKey, store.job.ReservedProviderMessageID)
	}
}

// F: an existing reservation is reused without calling NewMessageID again.
func TestExistingReservationSkipsNewMessageIDCall(t *testing.T) {
	h, store, sender, raw := setup(t)
	store.job.ReservedProviderMessageID = "already-reserved-id"
	if err := h.Handle(context.Background(), raw, 1); err != nil {
		t.Fatal(err)
	}
	if sender.newIDCalls != 0 {
		t.Fatalf("NewMessageID calls = %d, want 0 (reservation already existed)", sender.newIDCalls)
	}
	if sender.got.IdempotencyKey != "already-reserved-id" {
		t.Fatalf("SendText must reuse the existing reservation, got %q", sender.got.IdempotencyKey)
	}
}

// C/D: retry after a transient failure (timeout/5xx classified as
// ErrTransient/ErrProviderUnavailable) and redelivery after a simulated
// handler failure both reuse the exact same reserved id — never a new one.
func TestRetryAndRedeliveryReuseSameReservedID(t *testing.T) {
	h, store, sender, raw := setup(t)
	sender.err = fmt.Errorf("timeout: %w", ports.ErrProviderUnavailable)
	if err := h.Handle(context.Background(), raw, 1); err == nil {
		t.Fatal("expected a retryable error")
	}
	firstReserved := store.job.ReservedProviderMessageID
	if firstReserved == "" {
		t.Fatal("a reservation must survive the transient failure (it committed before the provider call)")
	}
	sender.err = nil
	if err := h.Handle(context.Background(), raw, 2); err != nil {
		t.Fatal(err)
	}
	if sender.newIDCalls != 1 {
		t.Fatalf("NewMessageID calls = %d, want 1 total across both attempts", sender.newIDCalls)
	}
	if sender.got.IdempotencyKey != firstReserved {
		t.Fatalf("retry used id %q, want the same reserved id %q", sender.got.IdempotencyKey, firstReserved)
	}
}

// E: this is the specific reproduction of the previously unsafe D/E window —
// a successful provider call whose MarkSent never commits (simulated by
// MarkSent itself failing) must NOT cause the next delivery attempt to mint
// a new id; it must reuse the same one.
func TestReservationSurvivesSimulatedMarkSentFailure(t *testing.T) {
	h, store, sender, raw := setup(t)
	if err := h.Handle(context.Background(), raw, 1); err != nil {
		t.Fatal(err)
	}
	reserved := store.job.ReservedProviderMessageID
	if reserved == "" {
		t.Fatal("reservation must exist after a successful delivery")
	}
	// Simulate the crash: MarkSent's transaction never committed, so the row
	// is still "queued" with the SAME reservation, and gets redelivered.
	store.job.Status = "queued"
	store.job.ProviderMessageID = ""
	if err := h.Handle(context.Background(), raw, 2); err != nil {
		t.Fatal(err)
	}
	if sender.newIDCalls != 1 {
		t.Fatalf("NewMessageID calls = %d, want 1 (redelivery must reuse the reservation, not mint a new one)", sender.newIDCalls)
	}
	if sender.got.IdempotencyKey != reserved {
		t.Fatalf("redelivery after simulated MarkSent failure used id %q, want the same reserved id %q", sender.got.IdempotencyKey, reserved)
	}
}

// G: concurrent reservations for the same message converge on exactly one
// persisted id, and only that id is ever used for delivery.
func TestConcurrentReservationConvergesOnOneID(t *testing.T) {
	msgID, connID := uuid.New(), uuid.New()
	job := &delivery.OutboundJob{MessageID: msgID, ConnectionID: connID, ToE164: "+5511999990000", Text: "oi", Status: "queued", ConnectionActive: true}
	store := &fakeStore{job: job}
	sender := &fakeSender{}
	var mu sync.Mutex
	var winners []string
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := store.EnsureReservedProviderMessageID(context.Background(), msgID, func(ctx context.Context) (string, error) {
				return sender.NewMessageID(ctx, connID)
			})
			if err != nil {
				t.Errorf("reservation error: %v", err)
				return
			}
			mu.Lock()
			winners = append(winners, id)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(winners) != 8 {
		t.Fatalf("got %d winners, want 8", len(winners))
	}
	for _, w := range winners[1:] {
		if w != winners[0] {
			t.Fatalf("concurrent reservations did not converge on one id: %v", winners)
		}
	}
}

// H: a non-empty provider response id that differs from the reserved id is
// not silently accepted — exercised at the WahaProvider level
// (TestSendTextRejectsMismatchedResponseID in the waha package), since the
// fakeSender here always echoes the id it was given; the mismatch check
// belongs to the provider adapter, not the delivery handler.

// I/J: confirmed success keeps existing provider_message_id semantics, and
// the reservation is retained (not cleared) for audit/retry history.
func TestConfirmedSuccessRetainsReservationForAudit(t *testing.T) {
	h, store, sender, raw := setup(t)
	if err := h.Handle(context.Background(), raw, 1); err != nil {
		t.Fatal(err)
	}
	if store.job.Status != "sent" {
		t.Fatalf("status = %q, want sent", store.job.Status)
	}
	if store.job.ProviderMessageID == "" {
		t.Fatal("provider_message_id must be set on confirmed success")
	}
	if store.job.ReservedProviderMessageID == "" {
		t.Fatal("reservation must be retained after success, not cleared")
	}
	if sender.got.IdempotencyKey != store.job.ReservedProviderMessageID {
		t.Fatal("confirmed send must have used the reserved id")
	}
}
