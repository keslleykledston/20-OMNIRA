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

type fakeExternal struct {
	sc       *ports.SendContext
	tpl      *ports.Template
	inserted int
	lastBody string
	version  int64
	inst     uuid.UUID
}

func (f *fakeExternal) LoadSendContext(context.Context, uuid.UUID) (*ports.SendContext, error) {
	return f.sc, nil
}
func (f *fakeExternal) LoadTemplate(context.Context, uuid.UUID, uuid.UUID) (*ports.Template, error) {
	return f.tpl, nil
}
func (f *fakeExternal) InsertQueuedExternal(_ context.Context, inst uuid.UUID, v int64, in ports.SendContext, body, hash string) (*ports.QueuedMessage, error) {
	f.inserted++
	f.lastBody, f.version, f.inst = body, v, inst
	return &ports.QueuedMessage{ID: uuid.New(), ConversationID: in.ConversationID, Body: body, RequestHash: hash}, nil
}
func (f *fakeExternal) InsertQueuedExternalTemplate(ctx context.Context, inst uuid.UUID, v int64, in ports.SendContext, body, hash string, _ ports.TemplateSend) (*ports.QueuedMessage, error) {
	return f.InsertQueuedExternal(ctx, inst, v, in, body, hash)
}

func externalCtx(source tenancydomain.AccessSource) context.Context {
	return tenancydomain.WithTenantContext(context.Background(), &tenancydomain.TenantContext{TenantID: uuid.New(), Source: source})
}

func okContext() *ports.SendContext {
	conn := uuid.New()
	last := time.Now().Add(-time.Hour)
	return &ports.SendContext{ConversationID: uuid.New(), ConnectionID: &conn, ConnectionReady: true, ToE164: "+5592900000000", Provider: "waha", LastInboundAt: &last}
}

// The sender keeps every provider-facing rule of a Workspace reply, by itself (the HTTP layer and the store check them again).
func TestExternalSenderKeepsTheChannelWindowAndTemplateRules(t *testing.T) {
	inst := uuid.New()
	msg := func(f *fakeExternal, m ExternalMessage) error {
		m.InstallationID = inst
		if m.ConversationID == uuid.Nil && f.sc != nil {
			m.ConversationID = f.sc.ConversationID
		}
		if m.ConversationID == uuid.Nil {
			m.ConversationID = uuid.New()
		}
		_, err := NewExternalSender(f).Send(externalCtx(tenancydomain.AccessSourceSystem), m)
		return err
	}
	f := &fakeExternal{sc: okContext()}
	if err := msg(f, ExternalMessage{Text: "olá", ControlVersion: 7}); err != nil || f.inserted != 1 || f.version != 7 || f.inst != inst {
		t.Fatalf("plain send: %v inserted=%d v=%d", err, f.inserted, f.version)
	}
	// only a system session may queue for an application
	for _, src := range []tenancydomain.AccessSource{tenancydomain.AccessSourceDirect} {
		if _, err := NewExternalSender(f).Send(externalCtx(src), ExternalMessage{InstallationID: inst, ConversationID: f.sc.ConversationID, Text: "x"}); !errors.Is(err, ErrForbidden) {
			t.Errorf("a %v session queued for an application: %v", src, err)
		}
	}
	if _, err := NewExternalSender(f).Send(externalCtx(tenancydomain.AccessSourceSystem), ExternalMessage{ConversationID: f.sc.ConversationID, Text: "x"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("no installation: %v", err)
	}
	// shape
	for name, m := range map[string]ExternalMessage{"empty": {Text: "  "}, "neither": {}, "both": {Text: "a", TemplateID: ptr(uuid.New())}} {
		if err := msg(&fakeExternal{sc: okContext()}, m); !errors.Is(err, ErrInvalidText) {
			t.Errorf("%s: %v", name, err)
		}
	}
	long := make([]rune, MaxTextRunes+1)
	for i := range long {
		long[i] = 'a'
	}
	if err := msg(&fakeExternal{sc: okContext()}, ExternalMessage{Text: string(long)}); !errors.Is(err, ErrInvalidText) {
		t.Errorf("too long: %v", err)
	}
	if err := msg(&fakeExternal{}, ExternalMessage{Text: "x", ConversationID: uuid.New()}); !errors.Is(err, ErrNotFound) {
		t.Errorf("no conversation: %v", err)
	}
	if err := msg(&fakeExternal{sc: okContext()}, ExternalMessage{Text: "x", ConversationID: uuid.Nil}); err != nil {
		t.Errorf("conversation id taken from the context: %v", err)
	}
	// closed, no channel, channel not ready, no phone
	closed := okContext()
	closed.Closed = true
	if err := msg(&fakeExternal{sc: closed}, ExternalMessage{Text: "x"}); !errors.Is(err, ErrConversationClosed) {
		t.Errorf("closed: %v", err)
	}
	for name, mod := range map[string]func(*ports.SendContext){"no connection": func(c *ports.SendContext) { c.ConnectionID = nil }, "not ready": func(c *ports.SendContext) { c.ConnectionReady = false },
		"no phone": func(c *ports.SendContext) { c.ToE164 = "" }} {
		sc := okContext()
		mod(sc)
		fx := &fakeExternal{sc: sc}
		if err := msg(fx, ExternalMessage{Text: "x"}); !errors.Is(err, ErrChannelUnavailable) || fx.inserted != 0 {
			t.Errorf("%s: %v inserted=%d", name, err, fx.inserted)
		}
	}
	// the 24 h window of the official provider
	old := okContext()
	old.Provider = ProviderMetaCloud
	stale := time.Now().Add(-25 * time.Hour)
	old.LastInboundAt = &stale
	fo := &fakeExternal{sc: old}
	if err := msg(fo, ExternalMessage{Text: "x"}); !errors.Is(err, ErrWindowClosed) || fo.inserted != 0 {
		t.Errorf("window closed: %v inserted=%d", err, fo.inserted)
	}
	fresh := time.Now().Add(-time.Hour)
	old.LastInboundAt = &fresh
	if err := msg(fo, ExternalMessage{Text: "x"}); err != nil {
		t.Errorf("window open: %v", err)
	}
	old.LastInboundAt = nil
	if err := msg(fo, ExternalMessage{Text: "x"}); !errors.Is(err, ErrWindowClosed) {
		t.Errorf("never wrote: %v", err)
	}
	// templates: only on the official provider, approved and sendable, with the right variables; allowed outside the window
	good := &ports.Template{ID: uuid.New(), Name: "n", Language: "pt_BR", Body: "Olá {{1}}", Status: "APPROVED", VariableCount: 1, Sendable: true}
	ft := &fakeExternal{sc: old, tpl: good}
	if err := msg(ft, ExternalMessage{TemplateID: &good.ID, TemplateParams: []string{"Ana"}}); err != nil || ft.lastBody != "Olá Ana" {
		t.Errorf("template outside the window: %v body=%q", err, ft.lastBody)
	}
	if err := msg(ft, ExternalMessage{TemplateID: &good.ID}); !errors.Is(err, ErrTemplateParams) {
		t.Errorf("missing variables: %v", err)
	}
	bad := *good
	bad.Status = "PENDING"
	if err := msg(&fakeExternal{sc: old, tpl: &bad}, ExternalMessage{TemplateID: &good.ID, TemplateParams: []string{"Ana"}}); !errors.Is(err, ErrTemplateNotAllowed) {
		t.Errorf("unapproved: %v", err)
	}
	bad = *good
	bad.Sendable = false
	if err := msg(&fakeExternal{sc: old, tpl: &bad}, ExternalMessage{TemplateID: &good.ID, TemplateParams: []string{"Ana"}}); !errors.Is(err, ErrTemplateNotAllowed) {
		t.Errorf("not sendable: %v", err)
	}
	if err := msg(&fakeExternal{sc: old}, ExternalMessage{TemplateID: &good.ID, TemplateParams: []string{"Ana"}}); !errors.Is(err, ErrTemplateNotAllowed) {
		t.Errorf("template of another line: %v", err)
	}
	waha := okContext()
	if err := msg(&fakeExternal{sc: waha, tpl: good}, ExternalMessage{TemplateID: &good.ID, TemplateParams: []string{"Ana"}}); !errors.Is(err, ErrTemplateUnsupported) {
		t.Errorf("template on a line without templates: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
