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
	tpl      *ports.Template
	tplSent  *ports.TemplateSend
}

func (f *fakeStore) LoadSendContext(context.Context, uuid.UUID) (*ports.SendContext, error) {
	return &f.sc, nil
}
func (f *fakeStore) InsertQueued(_ context.Context, _ uuid.UUID, in ports.SendContext, body, _, _ string, _ bool) (*ports.QueuedMessage, bool, error) {
	f.inserted++
	return &ports.QueuedMessage{ID: uuid.New(), ConversationID: in.ConversationID, Body: body, Status: "queued"}, false, nil
}

func (f *fakeStore) LoadTemplate(context.Context, uuid.UUID, uuid.UUID) (*ports.Template, error) {
	return f.tpl, nil
}
func (f *fakeStore) InsertQueuedTemplate(ctx context.Context, sender uuid.UUID, in ports.SendContext, body, key, hash string, req bool, tpl ports.TemplateSend) (*ports.QueuedMessage, bool, error) {
	f.tplSent = &tpl
	return f.InsertQueued(ctx, sender, in, body, key, hash, req)
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

// ADR-0020: a finalized conversation takes no more replies (the contact's next message starts a new attendance).
func TestSendRefusesAFinalizedConversationBeforeQueueing(t *testing.T) {
	user, conn, conv := uuid.New(), uuid.New(), uuid.New()
	tc, _ := tenancydomain.NewTenantContext(uuid.New(), user, tenancydomain.AccessSourceDirect)
	ctx := tenancydomain.WithTenantContext(context.Background(), tc)
	recent := time.Now().Add(-1 * time.Hour)
	store := &fakeStore{sc: ports.SendContext{ConversationID: conv, AssignedTo: &user, ConnectionID: &conn, ConnectionReady: true, ToE164: "+5592966660001", Provider: "waha", LastInboundAt: &recent, Closed: true}}
	if _, err := NewSender(store, allowAll{}).Send(ctx, conv, "olá", "key-12345678"); !errors.Is(err, ErrConversationClosed) {
		t.Fatalf("want ErrConversationClosed, got %v", err)
	}
	if store.inserted != 0 {
		t.Fatal("nothing may be queued")
	}
	store.sc.Closed = false
	if _, err := NewSender(store, allowAll{}).Send(ctx, conv, "olá", "key-12345678"); err != nil || store.inserted != 1 {
		t.Fatalf("an open conversation still sends: %v inserted=%d", err, store.inserted)
	}
}

func TestSendTemplateWorksOutsideTheWindowAndValidatesTheVariables(t *testing.T) {
	user, conn, conv, tid := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tc, _ := tenancydomain.NewTenantContext(uuid.New(), user, tenancydomain.AccessSourceDirect)
	ctx := tenancydomain.WithTenantContext(context.Background(), tc)
	old := time.Now().Add(-72 * time.Hour)
	mk := func(provider string, tpl *ports.Template) *fakeStore {
		return &fakeStore{tpl: tpl, sc: ports.SendContext{ConversationID: conv, AssignedTo: &user, ConnectionID: &conn, ConnectionReady: true, ToE164: "+5592966660001", Provider: provider, LastInboundAt: &old}}
	}
	good := &ports.Template{ID: tid, Name: "boas_vindas", Language: "pt_BR", Body: "Olá {{1}}, chamado {{2}}.", Status: "APPROVED", VariableCount: 2, Sendable: true}

	st := mk(ProviderMetaCloud, good)
	res, err := NewSender(st, allowAll{}).SendTemplate(ctx, conv, tid, []string{"Ana", "123"}, "key-12345678")
	if err != nil || st.tplSent == nil || st.tplSent.Name != "boas_vindas" || res.Message.Body != "Olá Ana, chamado 123." {
		t.Fatalf("a template is allowed with the window closed: err=%v sent=%v body=%q", err, st.tplSent, res.Message)
	}
	for name, c := range map[string]struct {
		st     *fakeStore
		params []string
		want   error
	}{
		"missing variable":  {mk(ProviderMetaCloud, good), []string{"Ana"}, ErrTemplateParams},
		"line break":        {mk(ProviderMetaCloud, good), []string{"Ana\nx", "1"}, ErrTemplateParams},
		"empty variable":    {mk(ProviderMetaCloud, good), []string{" ", "1"}, ErrTemplateParams},
		"not approved":      {mk(ProviderMetaCloud, &ports.Template{ID: tid, Name: "x", Language: "pt_BR", Body: "a", Status: "PENDING", Sendable: true}), nil, ErrTemplateNotAllowed},
		"not sendable":      {mk(ProviderMetaCloud, &ports.Template{ID: tid, Name: "x", Language: "pt_BR", Body: "a", Status: "APPROVED", Sendable: false}), nil, ErrTemplateNotAllowed},
		"other line":        {mk(ProviderMetaCloud, nil), nil, ErrTemplateNotAllowed},
		"unofficial number": {mk("waha", good), []string{"Ana", "1"}, ErrTemplateUnsupported},
	} {
		_, err := NewSender(c.st, allowAll{}).SendTemplate(ctx, conv, tid, c.params, "key-12345678")
		if !errors.Is(err, c.want) || c.st.inserted != 0 {
			t.Errorf("%s: err=%v inserted=%d", name, err, c.st.inserted)
		}
	}
}
