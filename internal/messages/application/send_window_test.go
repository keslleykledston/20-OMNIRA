package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/messages/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type allowAll struct{}

func (allowAll) HasPermission(context.Context, uuid.UUID, string) (bool, error) { return true, nil }

type fakeStore struct {
	sc       ports.SendContext
	inserted int
}

func (f *fakeStore) LoadSendContext(context.Context, uuid.UUID) (*ports.SendContext, error) {
	return &f.sc, nil
}
func (f *fakeStore) InsertQueued(_ context.Context, _ uuid.UUID, in ports.SendContext, body, _, _ string, _ bool) (*ports.QueuedMessage, bool, error) {
	f.inserted++
	return &ports.QueuedMessage{ID: uuid.New(), ConversationID: in.ConversationID, Body: body, Status: "queued"}, false, nil
}

func TestSendRefusesFreeTextOutsideTheMetaWindowBeforeQueueing(t *testing.T) {
	user, conn, conv := uuid.New(), uuid.New(), uuid.New()
	tc, _ := tenancydomain.NewTenantContext(uuid.New(), user, tenancydomain.AccessSourceDirect)
	ctx := tenancydomain.WithTenantContext(context.Background(), tc)
	old, recent := time.Now().Add(-30*time.Hour), time.Now().Add(-1*time.Hour)
	for name, tc := range map[string]struct {
		provider string
		last     *time.Time
		wantErr  error
	}{
		"meta, window closed": {ProviderMetaCloud, &old, ErrWindowClosed},
		"meta, no inbound":    {ProviderMetaCloud, nil, ErrWindowClosed},
		"meta, window open":   {ProviderMetaCloud, &recent, nil},
		"waha, any time":      {"waha", &old, nil},
	} {
		store := &fakeStore{sc: ports.SendContext{ConversationID: conv, AssignedTo: &user, ConnectionID: &conn, ConnectionReady: true, ToE164: "+5592966660001", Provider: tc.provider, LastInboundAt: tc.last}}
		_, err := NewSender(store, allowAll{}).Send(ctx, conv, "olá", "key-12345678")
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: err=%v want %v", name, err, tc.wantErr)
		}
		if tc.wantErr != nil && store.inserted != 0 {
			t.Errorf("%s: a refused send must not queue anything", name)
		}
	}
}
