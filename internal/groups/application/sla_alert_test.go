package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	channelsdomain "github.com/omnira/omnira/internal/channels/domain"
)

type fakeAlertRepo struct {
	ch  *AlertChannel
	err error
}

func (f *fakeAlertRepo) AlertChannelForConversation(ctx context.Context, conversationID uuid.UUID) (*AlertChannel, error) {
	return f.ch, f.err
}

type fakeSender struct {
	sent []channelsdomain.OutboundTextMessage
	conn uuid.UUID
	err  error
}

func (f *fakeSender) SendText(ctx context.Context, connID uuid.UUID, msg channelsdomain.OutboundTextMessage) (*channelsdomain.SendResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.conn = connID
	f.sent = append(f.sent, msg)
	return &channelsdomain.SendResult{}, nil
}

func TestSLAAlertNeverSendsWhenNoChannelIsConfigured(t *testing.T) {
	repo := &fakeAlertRepo{ch: nil}
	sender := &fakeSender{}
	s := NewSLAAlertService(repo, sender, nil)
	s.HandleBreach(context.Background(), uuid.New(), "first_response", "key-1")
	if len(sender.sent) != 0 {
		t.Fatalf("sent = %d, want 0 (no channel configured)", len(sender.sent))
	}
}

func TestSLAAlertNeverPropagatesALookupOrSendFailure(t *testing.T) {
	repo := &fakeAlertRepo{err: errors.New("boom")}
	sender := &fakeSender{}
	s := NewSLAAlertService(repo, sender, nil)
	s.HandleBreach(context.Background(), uuid.New(), "resolution", "key-2") // must not panic, must not block on error

	group := &AlertChannel{GroupID: uuid.New(), ConnectionID: uuid.New(), ProviderGroupID: "123@g.us"}
	repo2 := &fakeAlertRepo{ch: group}
	sender2 := &fakeSender{err: errors.New("waha down")}
	s2 := NewSLAAlertService(repo2, sender2, nil)
	s2.HandleBreach(context.Background(), uuid.New(), "queue_wait", "key-3")
	if len(sender2.sent) != 0 {
		t.Fatalf("sent = %d, want 0 (send failed)", len(sender2.sent))
	}
}

func TestSLAAlertSendsToTheConfiguredChannelWithTheIdempotencyKeyAndNeverClientContent(t *testing.T) {
	group := &AlertChannel{GroupID: uuid.New(), ConnectionID: uuid.New(), ProviderGroupID: "555@g.us"}
	repo := &fakeAlertRepo{ch: group}
	sender := &fakeSender{}
	s := NewSLAAlertService(repo, sender, nil)
	s.HandleBreach(context.Background(), uuid.New(), "first_response", "idem-key-1")
	if len(sender.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(sender.sent))
	}
	got := sender.sent[0]
	if got.ProviderChatID != "555@g.us" || got.IdempotencyKey != "idem-key-1" {
		t.Fatalf("message = %+v", got)
	}
	if sender.conn != group.ConnectionID {
		t.Fatalf("connection = %v, want %v", sender.conn, group.ConnectionID)
	}
	if !strings.Contains(got.Text, "primeira resposta") {
		t.Fatalf("text = %q, want it to name the metric", got.Text)
	}
}
