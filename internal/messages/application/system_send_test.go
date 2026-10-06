package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/messages/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type fakeSystemStore struct {
	sc       *ports.SendContext
	inserted int
	err      error
	replay   *ports.QueuedMessage
}

func (f *fakeSystemStore) LoadSendContext(context.Context, uuid.UUID) (*ports.SendContext, error) {
	return f.sc, nil
}
func (f *fakeSystemStore) InsertQueuedSystem(_ context.Context, in ports.SendContext, body, _, hash string) (*ports.QueuedMessage, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	if f.replay != nil {
		return f.replay, true, nil
	}
	f.inserted++
	return &ports.QueuedMessage{ID: uuid.New(), ConversationID: in.ConversationID, Body: body, RequestHash: hash}, false, nil
}

func sysCtx(source tenancydomain.AccessSource) context.Context {
	actor := uuid.New()
	if source == tenancydomain.AccessSourceSystem {
		actor = uuid.Nil
	}
	tc, _ := tenancydomain.NewTenantContext(uuid.New(), actor, source)
	return tenancydomain.WithTenantContext(context.Background(), tc)
}

func readyContext(provider string, lastInbound time.Time) *ports.SendContext {
	conn := uuid.New()
	return &ports.SendContext{ConversationID: uuid.New(), ConnectionID: &conn, ConnectionReady: true, ToE164: "+5511999999999", Provider: provider, LastInboundAt: &lastInbound}
}

func TestSystemSenderOnlyForSystemContexts(t *testing.T) {
	st := &fakeSystemStore{sc: readyContext("waha", time.Now())}
	s := NewSystemSender(st)
	if _, err := s.Send(sysCtx(tenancydomain.AccessSourceDirect), uuid.New(), "hi", "flow:key:00001"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a human context must not use the system sender: %v", err)
	}
	if _, err := s.Send(context.Background(), uuid.New(), "hi", "flow:key:00001"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("no tenant context: %v", err)
	}
	if st.inserted != 0 {
		t.Fatal("nothing may be queued")
	}
}

func TestSystemSenderValidationAndOutcomes(t *testing.T) {
	ctx := sysCtx(tenancydomain.AccessSourceSystem)
	conv := uuid.New()
	st := &fakeSystemStore{sc: readyContext("waha", time.Now())}
	s := NewSystemSender(st)
	if _, err := s.Send(ctx, conv, "hi", "bad key"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("key: %v", err)
	}
	if _, err := s.Send(ctx, conv, "   ", "flow:key:00001"); !errors.Is(err, ErrInvalidText) {
		t.Fatalf("blank: %v", err)
	}
	if _, err := s.Send(ctx, conv, strings.Repeat("a", MaxTextRunes+1), "flow:key:00001"); !errors.Is(err, ErrInvalidText) {
		t.Fatalf("too long: %v", err)
	}
	if got, err := s.Send(ctx, conv, "hi", "flow:key:00001"); err != nil || got != SystemQueued {
		t.Fatalf("queue: %v %v", got, err)
	}
	// Expected outcomes are values, not errors.
	st.sc = &ports.SendContext{ConversationID: conv}
	if got, err := s.Send(ctx, conv, "hi", "flow:key:00001"); err != nil || got != SystemNoChannel {
		t.Fatalf("no channel: %v %v", got, err)
	}
	st.sc = readyContext("meta_cloud", time.Now().Add(-30*time.Hour)) // Meta's 24 h window is closed
	if got, err := s.Send(ctx, conv, "hi", "flow:key:00001"); err != nil || got != SystemWindowClosed {
		t.Fatalf("closed window: %v %v", got, err)
	}
	st.sc = readyContext("waha", time.Now())
	st.err = ports.ErrConversationChanged // a human was assigned between the checks
	if got, err := s.Send(ctx, conv, "hi", "flow:key:00001"); err != nil || got != SystemNoChannel {
		t.Fatalf("assigned meanwhile must stay silent: %v %v", got, err)
	}
	st.err = nil
	st.replay = &ports.QueuedMessage{ConversationID: conv, RequestHash: "different"}
	if _, err := s.Send(ctx, conv, "hi", "flow:key:00001"); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("same key, different content: %v", err)
	}
}
