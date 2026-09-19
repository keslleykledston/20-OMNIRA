package delivery_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/worker/delivery"
)

type fakeRunner struct {
	got    uuid.UUID
	called bool
}

func (r *fakeRunner) RunForConnection(ctx context.Context, id uuid.UUID, fn func(context.Context) error) error {
	r.got, r.called = id, true
	return fn(ctx)
}

type fakeSender struct {
	gotID  uuid.UUID
	gotMsg domain.OutboundTextMessage
}

func (s *fakeSender) SendText(_ context.Context, id uuid.UUID, msg domain.OutboundTextMessage) (*domain.SendResult, error) {
	s.gotID, s.gotMsg = id, msg
	return &domain.SendResult{State: domain.DeliveryStateSent}, nil
}

func TestHandlerUsesConnectionReferenceAndIgnoresTenantClaim(t *testing.T) {
	connectionID := uuid.New()
	runner := &fakeRunner{}
	sender := &fakeSender{}
	h, err := delivery.NewHandler(runner, sender)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"tenant_id": uuid.NewString(),
		"payload": map[string]string{
			"connection_id": connectionID.String(), "to_e164": "+5511999999999",
			"text": "hello", "idempotency_key": "outbox-1",
		},
	})
	if err := h.Handle(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if !runner.called || runner.got != connectionID || sender.gotID != connectionID || sender.gotMsg.Text != "hello" {
		t.Fatalf("unexpected delivery: runner=%+v sender=%+v", runner, sender)
	}
}

func TestHandlerRejectsMalformedOrMissingConnectionReference(t *testing.T) {
	h, err := delivery.NewHandler(&fakeRunner{}, &fakeSender{})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"{}", "not-json", `{"payload":{"connection_id":"not-a-uuid"}}`} {
		if err := h.Handle(context.Background(), []byte(raw)); !errors.Is(err, ports.ErrPermanent) {
			t.Errorf("payload %q: expected permanent error, got %v", raw, err)
		}
	}
}

func TestRetryableDoesNotRetryTerminalProviderStates(t *testing.T) {
	if !delivery.Retryable(ports.ErrTransient) || !delivery.Retryable(ports.ErrRateLimited) {
		t.Fatal("transient provider failures should retry")
	}
	for _, err := range []error{ports.ErrAuthentication, ports.ErrConfiguration, ports.ErrSessionDisconnected, ports.ErrPermanent} {
		if delivery.Retryable(err) {
			t.Errorf("terminal error marked retryable: %v", err)
		}
	}
}
