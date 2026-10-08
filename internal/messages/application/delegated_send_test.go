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

type fakeOutbound struct {
	ports.OutboundStore
	sc       *ports.SendContext
	inserted int
	sender   uuid.UUID
	require  bool
	replay   *ports.QueuedMessage
}

func (f *fakeOutbound) LoadSendContext(context.Context, uuid.UUID) (*ports.SendContext, error) {
	return f.sc, nil
}
func (f *fakeOutbound) InsertQueued(_ context.Context, sender uuid.UUID, in ports.SendContext, body, _, hash string, requireAssignee bool) (*ports.QueuedMessage, bool, error) {
	f.sender, f.require = sender, requireAssignee
	if f.replay != nil {
		return f.replay, true, nil
	}
	f.inserted++
	return &ports.QueuedMessage{ID: uuid.New(), ConversationID: in.ConversationID, Body: body, RequestHash: hash}, false, nil
}

func delegatedCtx(source tenancydomain.AccessSource) context.Context {
	actor := uuid.Nil
	if source != tenancydomain.AccessSourceSystem {
		actor = uuid.New()
	}
	tc, _ := tenancydomain.NewTenantContext(uuid.New(), actor, source)
	return tenancydomain.WithTenantContext(context.Background(), tc)
}

func TestDelegatedSender(t *testing.T) {
	agent := uuid.New()
	conv := uuid.New()
	ready := func(assigned *uuid.UUID) *ports.SendContext {
		sc := readyContext("waha", time.Now())
		sc.ConversationID = conv
		sc.AssignedTo = assigned
		return sc
	}
	sys := delegatedCtx(tenancydomain.AccessSourceSystem)

	t.Run("only a system context may use it (a human or an empty context may not)", func(t *testing.T) {
		st := &fakeOutbound{sc: ready(&agent)}
		d := NewDelegatedSender(st)
		for name, ctx := range map[string]context.Context{"direct": delegatedCtx(tenancydomain.AccessSourceDirect), "none": context.Background()} {
			if _, err := d.Send(ctx, agent, conv, "oi", "key-12345678", nil); !errors.Is(err, ErrForbidden) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, err := d.Send(sys, uuid.Nil, conv, "oi", "key-12345678", nil); !errors.Is(err, ErrForbidden) {
			t.Errorf("nil actor: %v", err)
		}
		if st.inserted != 0 {
			t.Fatal("nothing may be queued")
		}
	})
	t.Run("shape of the request", func(t *testing.T) {
		d := NewDelegatedSender(&fakeOutbound{sc: ready(&agent)})
		if _, err := d.Send(sys, agent, conv, "oi", "bad key", nil); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("key: %v", err)
		}
		if _, err := d.Send(sys, agent, conv, "  ", "key-12345678", nil); !errors.Is(err, ErrInvalidText) {
			t.Errorf("blank: %v", err)
		}
		if _, err := d.Send(sys, agent, conv, strings.Repeat("a", MaxTextRunes+1), "key-12345678", nil); !errors.Is(err, ErrInvalidText) {
			t.Errorf("long: %v", err)
		}
	})
	t.Run("the conversation must be the agent's: unassigned and someone else's are refused, no manager bypass", func(t *testing.T) {
		other := uuid.New()
		for name, c := range map[string]struct {
			assigned *uuid.UUID
			want     error
		}{"unassigned": {nil, ErrUnassigned}, "other agent": {&other, ErrNotAssignedToYou}} {
			st := &fakeOutbound{sc: ready(c.assigned)}
			if _, err := NewDelegatedSender(st).Send(sys, agent, conv, "oi", "key-12345678", nil); !errors.Is(err, c.want) || st.inserted != 0 {
				t.Errorf("%s: %v inserted=%d", name, err, st.inserted)
			}
		}
	})
	t.Run("the guard decides before anything is written, and its error is returned as is", func(t *testing.T) {
		st := &fakeOutbound{sc: ready(&agent)}
		stop := errors.New("delegation gone")
		if _, err := NewDelegatedSender(st).Send(sys, agent, conv, "oi", "key-12345678", func(context.Context, *ports.SendContext) error { return stop }); !errors.Is(err, stop) || st.inserted != 0 {
			t.Fatalf("%v inserted=%d", err, st.inserted)
		}
	})
	t.Run("closed conversation, missing channel and closed 24 h window", func(t *testing.T) {
		closed := ready(&agent)
		closed.Closed = true
		noChannel := ready(&agent)
		noChannel.ConnectionID = nil
		stale := readyContext("meta_cloud", time.Now().Add(-30*time.Hour))
		stale.ConversationID, stale.AssignedTo = conv, &agent
		for name, c := range map[string]struct {
			sc   *ports.SendContext
			want error
		}{"closed": {closed, ErrConversationClosed}, "no channel": {noChannel, ErrChannelUnavailable}, "window": {stale, ErrWindowClosed}} {
			if _, err := NewDelegatedSender(&fakeOutbound{sc: c.sc}).Send(sys, agent, conv, "oi", "key-12345678", nil); !errors.Is(err, c.want) {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
	t.Run("queues as the agent, requiring the assignee atomically; a replay with other content is a mismatch", func(t *testing.T) {
		st := &fakeOutbound{sc: ready(&agent)}
		d := NewDelegatedSender(st)
		res, err := d.Send(sys, agent, conv, "oi", "key-12345678", nil)
		if err != nil || res.Replayed || st.sender != agent || !st.require {
			t.Fatalf("res=%+v err=%v sender=%v require=%v", res, err, st.sender, st.require)
		}
		st.replay = &ports.QueuedMessage{ConversationID: conv, RequestHash: "different"}
		if _, err := d.Send(sys, agent, conv, "oi", "key-12345678", nil); !errors.Is(err, ErrIdempotencyMismatch) {
			t.Fatalf("mismatch: %v", err)
		}
	})
}
