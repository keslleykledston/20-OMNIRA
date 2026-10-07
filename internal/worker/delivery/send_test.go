package delivery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/worker/delivery"
)

// captureLogs (PILOT.4B) — a small local capture is enough here; no general
// logging-test framework is warranted for one package's log lines.
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)
	fn()
	return buf.String()
}

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
func (s *fakeStore) MarkUncertain(_ context.Context, _ uuid.UUID, reason string) error {
	s.failure = reason
	s.job.Status = "uncertain"
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

	templateGot    *domain.OutboundTemplateMessage
	interactiveGot *domain.OutboundInteractiveMessage
	interactiveErr error
	newIDCalls     int
	newIDTotal     int
	newIDErr       error
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

func (s *fakeSender) SendTemplate(_ context.Context, conn uuid.UUID, msg domain.OutboundTemplateMessage) (*domain.SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.total++
	s.templateGot = &msg
	if s.err != nil {
		return nil, s.err
	}
	return &domain.SendResult{ProviderMessageID: fmt.Sprintf("tpl-%s-%d", conn, s.total), State: domain.DeliveryStateSent}, nil
}

func (s *fakeSender) SendInteractive(_ context.Context, conn uuid.UUID, msg domain.OutboundInteractiveMessage) (*domain.SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.total++
	s.interactiveGot = &msg
	if s.interactiveErr != nil {
		return nil, s.interactiveErr
	}
	return &domain.SendResult{ProviderMessageID: fmt.Sprintf("itx-%s-%d", conn, s.total), State: domain.DeliveryStateSent}, nil
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

// setupWithMessageID is setup's twin for PILOT.4B's correlation walkthrough
// test, which needs to feed a message_id produced by a different package
// (messages/application's Sender.Send) into the delivery worker's fakes.
func setupWithMessageID(t *testing.T, msgID, connID uuid.UUID) (*delivery.Handler, *fakeStore, *fakeSender, []byte) {
	store := &fakeStore{job: &delivery.OutboundJob{MessageID: msgID, ConnectionID: connID, ToE164: "+5511999990000", Text: "oi", Status: "queued", ConnectionActive: true}}
	sender := &fakeSender{}
	h, err := delivery.NewHandler(store, sender, 3)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"aggregate_id": msgID.String()})
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

// PILOT.4A2 tests F/G/H: every attempt for a message ends in a non-confirming
// (but never disproven) outcome — repeated transport timeout, repeated 5xx,
// and repeated rate-limiting all retry with the same reserved id and, once
// the budget is exhausted, land on 'uncertain' — never 'failed', since
// nothing here proves the provider rejected the message.
func TestUncertainOnRetryableExhaustion(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		reason string
	}{
		"transport_timeout": {fmt.Errorf("timeout: %w", ports.ErrTransient), "outcome_unknown:transient"},
		"provider_5xx":      {fmt.Errorf("boom: %w", ports.ErrProviderUnavailable), "outcome_unknown:provider_unavailable"},
		"rate_limited":      {fmt.Errorf("slow down: %w", ports.ErrRateLimited), "outcome_unknown:rate_limited"},
	} {
		t.Run(name, func(t *testing.T) {
			h, store, sender, raw := setup(t)
			sender.err = tc.err
			for attempt := 1; attempt <= 2; attempt++ {
				err := h.Handle(context.Background(), raw, attempt)
				if err == nil || errors.Is(err, delivery.ErrPermanent) {
					t.Fatalf("attempt %d: want retryable error, got %v", attempt, err)
				}
				if store.job.Status != "queued" || store.commitOK {
					t.Fatal("a retry must leave the message queued and roll back")
				}
			}
			firstReserved := store.job.ReservedProviderMessageID
			if err := h.Handle(context.Background(), raw, 3); err != nil {
				t.Fatalf("final attempt must record the uncertain outcome and ack: %v", err)
			}
			if store.job.Status != "uncertain" || store.failure != tc.reason {
				t.Fatalf("status=%s reason=%q, want uncertain / %q", store.job.Status, store.failure, tc.reason)
			}
			if store.job.ReservedProviderMessageID != firstReserved {
				t.Fatal("the reserved id must be retained, not cleared, on an uncertain outcome")
			}
			if sender.newIDCalls != 1 {
				t.Fatalf("NewMessageID calls = %d, want 1 total across every attempt", sender.newIDCalls)
			}
		})
	}
}

// PILOT.4A2 tests I/J/K/L: an undecodable response and an empty response id
// both surface as ErrUnknown from the sender's perspective (the concrete
// distinction is exercised at the waha client level —
// TestClientSendTextRejectsEmptyResponseID). Both are safe to retry with the
// same reserved id and, if never resolved, exhaust to 'uncertain' — but a
// later confirming attempt still resolves to 'sent'.
func TestErrUnknownRetriesThenSucceeds(t *testing.T) {
	h, store, sender, raw := setup(t)
	sender.err = ports.ErrUnknown
	if err := h.Handle(context.Background(), raw, 1); err == nil {
		t.Fatal("an ErrUnknown outcome must be retried, not terminated immediately")
	}
	if store.job.Status != "queued" {
		t.Fatalf("status=%s, want queued (retry must roll back)", store.job.Status)
	}
	reserved := store.job.ReservedProviderMessageID
	if reserved == "" {
		t.Fatal("the reservation must survive the ambiguous attempt")
	}
	sender.err = nil
	if err := h.Handle(context.Background(), raw, 2); err != nil {
		t.Fatal(err)
	}
	if store.job.Status != "sent" {
		t.Fatalf("status=%s, want sent once a confirming attempt occurs", store.job.Status)
	}
	if sender.got.IdempotencyKey != reserved || sender.newIDCalls != 1 {
		t.Fatalf("retry must reuse the same reserved id %q, got %q (newIDCalls=%d)", reserved, sender.got.IdempotencyKey, sender.newIDCalls)
	}
}

func TestErrUnknownExhaustionBecomesUncertain(t *testing.T) {
	h, store, sender, raw := setup(t)
	sender.err = ports.ErrUnknown
	for attempt := 1; attempt <= 2; attempt++ {
		if err := h.Handle(context.Background(), raw, attempt); err == nil {
			t.Fatalf("attempt %d: want a retryable error", attempt)
		}
	}
	if err := h.Handle(context.Background(), raw, 3); err != nil {
		t.Fatal(err)
	}
	if store.job.Status != "uncertain" || store.failure != "outcome_unknown:unknown" {
		t.Fatalf("status=%s reason=%q, want uncertain / outcome_unknown:unknown", store.job.Status, store.failure)
	}
}

// PILOT.4A2 test M (central safety assertion, §15): a provider-id mismatch
// terminates as 'uncertain' on the VERY FIRST occurrence — never retried,
// regardless of remaining attempt budget — because retrying is not proven
// safe the way same-id redelivery is.
func TestProviderIDMismatchIsUncertainImmediatelyWithoutRetry(t *testing.T) {
	h, store, sender, raw := setup(t)
	sender.err = ports.ErrProviderIDMismatch
	if err := h.Handle(context.Background(), raw, 1); err != nil {
		t.Fatalf("a provider id mismatch must be acked as a terminal outcome, not retried: %v", err)
	}
	if store.job.Status != "uncertain" || store.failure != "outcome_unknown:provider_id_mismatch" {
		t.Fatalf("status=%s reason=%q, want uncertain / outcome_unknown:provider_id_mismatch", store.job.Status, store.failure)
	}
	if sender.calls != 1 {
		t.Fatalf("SendText calls = %d, want exactly 1 — no automatic retry after a mismatch", sender.calls)
	}
	// A later Handle() call (e.g. a spurious redelivery) must not call the
	// provider again: the message is already terminal.
	if err := h.Handle(context.Background(), raw, 2); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 {
		t.Fatalf("SendText calls = %d after a second Handle() on an uncertain message, want still 1", sender.calls)
	}
}

// PILOT.4A2 test N/O: once a message is 'uncertain', further Handle() calls
// never call the provider again, and the reservation stays intact.
func TestUncertainMessageIsNeverRedelivered(t *testing.T) {
	h, store, sender, raw := setup(t)
	store.job.Status = "uncertain"
	store.job.ReservedProviderMessageID = "already-reserved-id"
	if err := h.Handle(context.Background(), raw, 1); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 0 || sender.newIDCalls != 0 {
		t.Fatalf("an uncertain message must never be redelivered: sendCalls=%d newIDCalls=%d", sender.calls, sender.newIDCalls)
	}
	if store.job.ReservedProviderMessageID != "already-reserved-id" {
		t.Fatal("the reservation must remain untouched on an uncertain message")
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

// --- PILOT.4B: outbound correlation logging ------------------------------

// A: a confirmed success log line carries message_id.
func TestLogSentContainsMessageID(t *testing.T) {
	h, store, _, raw := setup(t)
	logs := captureLogs(t, func() {
		if err := h.Handle(context.Background(), raw, 1); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(logs, "message_id="+store.job.MessageID.String()) {
		t.Fatalf("sent log missing message_id: %s", logs)
	}
	if !strings.Contains(logs, "outcome=sent") {
		t.Fatalf("sent log missing outcome=sent: %s", logs)
	}
}

// B: a retry log line carries message_id and a normalized error class (never
// the raw error text, which could carry provider detail).
func TestLogRetryContainsMessageIDAndErrorClass(t *testing.T) {
	h, store, sender, raw := setup(t)
	sender.err = fmt.Errorf("upstream said something with an internal detail: %w", ports.ErrProviderUnavailable)
	logs := captureLogs(t, func() {
		if err := h.Handle(context.Background(), raw, 1); err == nil {
			t.Fatal("expected a retryable error")
		}
	})
	if !strings.Contains(logs, "message_id="+store.job.MessageID.String()) {
		t.Fatalf("retry log missing message_id: %s", logs)
	}
	if !strings.Contains(logs, "outcome=retry") || !strings.Contains(logs, "error_class=provider_unavailable") {
		t.Fatalf("retry log missing normalized outcome/error_class: %s", logs)
	}
	if strings.Contains(logs, "internal detail") {
		t.Fatalf("retry log leaked raw error text instead of a normalized class: %s", logs)
	}
}

// C: a definitive-failure terminal log line carries message_id.
func TestLogFailedContainsMessageID(t *testing.T) {
	h, store, sender, raw := setup(t)
	sender.err = ports.ErrAuthentication
	logs := captureLogs(t, func() {
		if err := h.Handle(context.Background(), raw, 1); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(logs, "message_id="+store.job.MessageID.String()) {
		t.Fatalf("failed log missing message_id: %s", logs)
	}
	if !strings.Contains(logs, "outcome=failed") || !strings.Contains(logs, "error_class=authentication") {
		t.Fatalf("failed log missing normalized outcome/error_class: %s", logs)
	}
}

// D: an uncertain terminal log line carries message_id.
func TestLogUncertainContainsMessageID(t *testing.T) {
	h, store, sender, raw := setup(t)
	sender.err = ports.ErrUnknown
	logs := captureLogs(t, func() {
		for attempt := 1; attempt <= 3; attempt++ {
			_ = h.Handle(context.Background(), raw, attempt)
		}
	})
	if !strings.Contains(logs, "message_id="+store.job.MessageID.String()) {
		t.Fatalf("uncertain log missing message_id: %s", logs)
	}
	if !strings.Contains(logs, "outcome=uncertain") || !strings.Contains(logs, "error_class=outcome_unknown:unknown") {
		t.Fatalf("uncertain log missing normalized outcome/error_class: %s", logs)
	}
}

// F/G: no message body or recipient phone ever appears in delivery logs,
// across every outcome exercised above.
func TestLogsNeverLeakBodyOrPhone(t *testing.T) {
	h, _, sender, raw := setup(t)
	var all strings.Builder
	all.WriteString(captureLogs(t, func() { sender.err = ports.ErrAuthentication; _ = h.Handle(context.Background(), raw, 1) }))
	h2, _, _, raw2 := setup(t)
	all.WriteString(captureLogs(t, func() { _ = h2.Handle(context.Background(), raw2, 1) }))
	h3, _, sender3, raw3 := setup(t)
	sender3.err = ports.ErrProviderIDMismatch
	all.WriteString(captureLogs(t, func() { _ = h3.Handle(context.Background(), raw3, 1) }))

	logs := all.String()
	if strings.Contains(logs, "oi") {
		t.Fatalf("delivery logs leaked the message body: %s", logs)
	}
	if strings.Contains(logs, "+5511999990000") {
		t.Fatalf("delivery logs leaked the recipient phone: %s", logs)
	}
}

// N (delivery side of I/J/K/L exhaustion): terminalizing to uncertain
// consumes the message; a further Handle() call never re-invokes the
// provider and logs the skip, not a new attempt.
func TestLogSkipWhenAlreadyTerminal(t *testing.T) {
	h, store, sender, raw := setup(t)
	store.job.Status = "uncertain"
	logs := captureLogs(t, func() {
		if err := h.Handle(context.Background(), raw, 1); err != nil {
			t.Fatal(err)
		}
	})
	if sender.calls != 0 {
		t.Fatalf("provider called %d times for an already-terminal message", sender.calls)
	}
	if !strings.Contains(logs, "message_id="+store.job.MessageID.String()) || !strings.Contains(logs, "status=uncertain") {
		t.Fatalf("skip log missing message_id/status: %s", logs)
	}
}

// Meta Cloud has no idempotency key: an ambiguous send (timeout/5xx after the request left) is terminal 'uncertain'
// on the first occurrence and the provider is never called again.
func TestOutcomeUnknownIsUncertainWithoutRetry(t *testing.T) {
	h, store, sender, raw := setup(t)
	sender.err = ports.ErrOutcomeUnknown
	if err := h.Handle(context.Background(), raw, 1); err != nil {
		t.Fatalf("an unknown outcome must be acked as terminal, not retried: %v", err)
	}
	if store.job.Status != "uncertain" || store.failure != "outcome_unknown:ambiguous_send" {
		t.Fatalf("status=%s reason=%q", store.job.Status, store.failure)
	}
	if err := h.Handle(context.Background(), raw, 2); err != nil || sender.calls != 1 {
		t.Fatalf("calls=%d err=%v, want exactly 1 call", sender.calls, err)
	}
}

// A queued template message goes to SendTemplate with its name, language and variables; SendText is not called.
func TestTemplateJobIsSentAsATemplateNotAsText(t *testing.T) {
	h, store, sender, raw := setup(t)
	store.job.Template = &delivery.TemplateJob{Name: "boas_vindas", Language: "pt_BR", Params: []string{"Ana", "123"}}
	store.job.Text = "Olá Ana, seu chamado 123 foi aberto."
	if err := h.Handle(context.Background(), raw, 1); err != nil {
		t.Fatal(err)
	}
	if store.job.Status != "sent" || sender.templateGot == nil || sender.got.Text != "" {
		t.Fatalf("status=%s template=%v text=%q", store.job.Status, sender.templateGot, sender.got.Text)
	}
	if g := sender.templateGot; g.TemplateName != "boas_vindas" || g.LanguageCode != "pt_BR" || len(g.Params) != 2 || g.Params[0] != "Ana" {
		t.Fatalf("template payload: %+v", g)
	}
}

// A bot menu goes out as buttons; on a provider without buttons the SAME menu is sent as numbered text, once.
func TestInteractiveJobIsSentAsButtonsAndFallsBackToTextWhenUnsupported(t *testing.T) {
	job := &delivery.InteractiveJob{Body: "Como ajudar?", ListLabel: "Ver opções", Options: []domain.InteractiveOption{{ID: "tech", Title: "Suporte"}, {ID: "fin", Title: "Financeiro"}}}
	h, store, sender, raw := setup(t)
	store.job.Interactive = job
	store.job.Text = "Como ajudar?\n1) Suporte\n2) Financeiro"
	if err := h.Handle(context.Background(), raw, 1); err != nil {
		t.Fatal(err)
	}
	if store.job.Status != "sent" || sender.interactiveGot == nil || sender.got.Text != "" || len(sender.interactiveGot.Options) != 2 {
		t.Fatalf("buttons expected: status=%s itx=%v text=%q", store.job.Status, sender.interactiveGot, sender.got.Text)
	}

	h2, store2, sender2, raw2 := setup(t)
	store2.job.Interactive = job
	store2.job.Text = "Como ajudar?\n1) Suporte\n2) Financeiro"
	sender2.interactiveErr = ports.ErrCapabilityNotSupported
	if err := h2.Handle(context.Background(), raw2, 1); err != nil {
		t.Fatal(err)
	}
	if store2.job.Status != "sent" || sender2.got.Text != "Como ajudar?\n1) Suporte\n2) Financeiro" {
		t.Fatalf("fallback to the numbered text expected: status=%s text=%q", store2.job.Status, sender2.got.Text)
	}
}
