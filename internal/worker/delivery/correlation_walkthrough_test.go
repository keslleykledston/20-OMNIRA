package delivery_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	messagesapp "github.com/omnira/omnira/internal/messages/application"
	messagesports "github.com/omnira/omnira/internal/messages/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// fakeSendStore — the minimal ports.OutboundStore double needed to exercise
// messages/application.Sender.Send's own accept log without a real Postgres
// dependency; the delivery-side fakes it feeds into are send_test.go's.
type fakeSendStore struct {
	sc *messagesports.SendContext
}

func (f *fakeSendStore) LoadSendContext(context.Context, uuid.UUID) (*messagesports.SendContext, error) {
	return f.sc, nil
}

func (f *fakeSendStore) InsertQueued(_ context.Context, _ uuid.UUID, in messagesports.SendContext, body, _, requestHash string, _ bool) (*messagesports.QueuedMessage, bool, error) {
	return &messagesports.QueuedMessage{ID: uuid.New(), ConversationID: in.ConversationID, Body: body, Status: "queued", RequestHash: requestHash}, false, nil
}

type allowAllPermissions struct{}

func (allowAllPermissions) HasPermission(context.Context, uuid.UUID, string) (bool, error) {
	return true, nil
}

// PILOT.4B §11: correlation walkthrough. Using only fakes/test data (no real
// Postgres, no real WhatsApp send, no real HTTP), prove that an operator
// handed a single message_id can follow it from acceptance
// (messages/application.Sender.Send) through to a confirmed-sent delivery
// (worker/delivery.Handler.Handle) — two different packages, one durable
// correlation key.
func TestCorrelationWalkthroughMessageIDLinksAcceptanceToDelivery(t *testing.T) {
	tenantID, actorID, connID, convID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	assignedTo := actorID
	sendStore := &fakeSendStore{sc: &messagesports.SendContext{
		ConversationID: convID, AssignedTo: &assignedTo, ConnectionID: &connID, ConnectionReady: true, ToE164: "+5511999990000",
	}}
	sender := messagesapp.NewSender(sendStore, allowAllPermissions{})
	tc, err := tenancydomain.NewTenantContext(tenantID, actorID, tenancydomain.AccessSourceDirect)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenancydomain.WithTenantContext(context.Background(), tc)

	var result messagesapp.SendResult
	acceptLogs := captureLogs(t, func() {
		var sendErr error
		result, sendErr = sender.Send(ctx, convID, "oi", "idem-key-walkthrough1")
		if sendErr != nil {
			t.Fatal(sendErr)
		}
	})
	messageID := result.Message.ID
	if !strings.Contains(acceptLogs, "messages: accepted message_id="+messageID.String()) {
		t.Fatalf("stage A (accepted): message_id not found:\n%s", acceptLogs)
	}

	// Stage B (outbox publish) is not exercised here — it has no fake in this
	// package and is proven separately (publisher_test.go's
	// TestMessageIDSuffixOnlyForOutboundSendJob). The durable correlation key
	// (message_id) is identical across both packages by construction: it's
	// literally the outbox event's aggregate_id (messages/adapters/postgres.go).

	h, store, _, raw := setupWithMessageID(t, messageID, connID)
	deliveryLogs := captureLogs(t, func() {
		if err := h.Handle(context.Background(), raw, 1); err != nil {
			t.Fatal(err)
		}
	})
	key := "message_id=" + messageID.String()
	if !strings.Contains(deliveryLogs, "channel delivery: attempt started "+key) {
		t.Fatalf("stage C (attempt started): %s not found:\n%s", key, deliveryLogs)
	}
	if !strings.Contains(deliveryLogs, "channel delivery: sent "+key) {
		t.Fatalf("stage E (sent): %s not found:\n%s", key, deliveryLogs)
	}
	if store.job.Status != "sent" {
		t.Fatalf("delivery outcome = %q, want sent", store.job.Status)
	}

	all := acceptLogs + deliveryLogs
	if strings.Contains(all, "oi") {
		t.Fatalf("correlation walkthrough logs leaked the message body:\n%s", all)
	}
	if strings.Contains(all, "+5511999990000") {
		t.Fatalf("correlation walkthrough logs leaked the recipient phone:\n%s", all)
	}
}
