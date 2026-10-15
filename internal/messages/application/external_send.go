package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/messages/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ExternalSender queues outbound messages for an external application (ADR-0048 H5). It is separate from Sender (operators: conversation.claim, the
// assignee) and SystemSender (automation) on purpose, and reuses everything that is provider-facing: the same 24 h customer-service window, the same
// approved-template rules and the same delivery job, so what leaves for WhatsApp is identical to what a Workspace or Hub reply produces.
//
// It never talks to a provider (202 means "queued", nothing more) and it never checks WHO may write: the caller (the external write path) has already
// re-checked the installation, the scope, the visibility and the hold in this transaction, and the store re-proves the hold and the control version in the
// very statement that inserts the message.
type ExternalSender struct{ store ports.ExternalOutboundStore }

func NewExternalSender(store ports.ExternalOutboundStore) *ExternalSender {
	return &ExternalSender{store: store}
}

// ExternalMessage is one message to queue. Exactly one of Text and TemplateID is set.
type ExternalMessage struct {
	InstallationID uuid.UUID
	ConversationID uuid.UUID
	ControlVersion int64
	Text           string
	TemplateID     *uuid.UUID
	TemplateParams []string
	Now            time.Time // zero: the clock
}

func (s *ExternalSender) Send(ctx context.Context, m ExternalMessage) (*ports.QueuedMessage, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.Source != tenancydomain.AccessSourceSystem || m.InstallationID == uuid.Nil {
		return nil, ErrForbidden // only the external write path, in a system session, queues for an application
	}
	if m.ConversationID == uuid.Nil {
		return nil, ErrNotFound
	}
	if (m.TemplateID == nil) == (strings.TrimSpace(m.Text) == "") {
		return nil, ErrInvalidText // exactly one of text and template
	}
	if m.TemplateID == nil && utf8.RuneCountInString(m.Text) > MaxTextRunes {
		return nil, ErrInvalidText
	}
	sc, err := s.store.LoadSendContext(ctx, m.ConversationID)
	if err != nil {
		return nil, err
	}
	if sc == nil {
		return nil, ErrNotFound
	}
	if sc.Closed {
		return nil, ErrConversationClosed
	}
	if sc.ConnectionID == nil || !sc.ConnectionReady || sc.ToE164 == "" {
		return nil, ErrChannelUnavailable
	}
	now := m.Now
	if now.IsZero() {
		now = time.Now()
	}
	if m.TemplateID == nil {
		if _, open := SessionWindow(sc.Provider, sc.LastInboundAt, now); !open {
			return nil, ErrWindowClosed
		}
		sum := sha256.Sum256([]byte(m.ConversationID.String() + "\n" + m.Text))
		msg, err := s.store.InsertQueuedExternal(ctx, m.InstallationID, m.ControlVersion, *sc, m.Text, hex.EncodeToString(sum[:]))
		if err != nil {
			return nil, err
		}
		log.Printf("messages: accepted external message_id=%s tenant_id=%s conversation_id=%s outcome=queued", msg.ID, tc.TenantID, m.ConversationID)
		return msg, nil
	}
	// a template is allowed inside or outside the window (that is how a conversation reaches someone who has not written lately)
	if sc.Provider != ProviderMetaCloud {
		return nil, ErrTemplateUnsupported
	}
	tpl, err := s.store.LoadTemplate(ctx, *sc.ConnectionID, *m.TemplateID)
	if err != nil {
		return nil, err
	}
	if tpl == nil || tpl.Status != "APPROVED" || !tpl.Sendable {
		return nil, ErrTemplateNotAllowed
	}
	body, err := RenderTemplate(tpl.Body, tpl.VariableCount, m.TemplateParams)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(m.ConversationID.String() + "\ntpl:" + m.TemplateID.String() + "\n" + strings.Join(m.TemplateParams, "\x1f")))
	msg, err := s.store.InsertQueuedExternalTemplate(ctx, m.InstallationID, m.ControlVersion, *sc, body, hex.EncodeToString(sum[:]),
		ports.TemplateSend{Name: tpl.Name, Language: tpl.Language, Params: m.TemplateParams, ID: tpl.ID, ConnectionID: *sc.ConnectionID})
	if err != nil {
		if errors.Is(err, ports.ErrTemplateGone) {
			return nil, ErrTemplateNotAllowed
		}
		return nil, err
	}
	log.Printf("messages: accepted external template message_id=%s tenant_id=%s conversation_id=%s outcome=queued", msg.ID, tc.TenantID, m.ConversationID)
	return msg, nil
}
