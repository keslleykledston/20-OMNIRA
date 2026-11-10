package groupalert

import (
	"context"
	"testing"

	"github.com/google/uuid"

	channelsdomain "github.com/omnira/omnira/internal/channels/domain"
	groupsapp "github.com/omnira/omnira/internal/groups/application"
)

type fakeRepo struct{ ch *groupsapp.AlertChannel }

func (f *fakeRepo) AlertChannelForConversation(ctx context.Context, conversationID uuid.UUID) (*groupsapp.AlertChannel, error) {
	return f.ch, nil
}

type fakeSender struct {
	sent []channelsdomain.OutboundTextMessage
}

func (f *fakeSender) SendText(ctx context.Context, connID uuid.UUID, msg channelsdomain.OutboundTextMessage) (*channelsdomain.SendResult, error) {
	f.sent = append(f.sent, msg)
	return &channelsdomain.SendResult{}, nil
}

func TestHandleIgnoresAMalformedEnvelopeWithoutError(t *testing.T) {
	sender := &fakeSender{}
	h := NewHandler(groupsapp.NewSLAAlertService(&fakeRepo{}, nil, sender, nil))
	for _, raw := range []string{`not json`, `{}`, `{"aggregate_id":"not-a-uuid"}`, `{"aggregate_id":"` + uuid.Nil.String() + `"}`} {
		h.Handle(context.Background(), []byte(raw)) // must never panic
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent = %d, want 0", len(sender.sent))
	}
}

func TestHandleSendsForAWellFormedEnvelopeWhenAChannelIsConfigured(t *testing.T) {
	group := &groupsapp.AlertChannel{GroupID: uuid.New(), ConnectionID: uuid.New(), ProviderGroupID: "999@g.us"}
	sender := &fakeSender{}
	h := NewHandler(groupsapp.NewSLAAlertService(&fakeRepo{ch: group}, nil, sender, nil))
	conv := uuid.New()
	raw := `{"id":"evt-1","aggregate_id":"` + conv.String() + `","payload":{"metric":"assignment"}}`
	h.Handle(context.Background(), []byte(raw))
	if len(sender.sent) != 1 || sender.sent[0].ProviderChatID != "999@g.us" || sender.sent[0].IdempotencyKey != "evt-1" {
		t.Fatalf("sent = %+v", sender.sent)
	}
}
