package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/messages/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// SystemSendStatus is the outcome of a bot send. Closed window and missing channel are expected business outcomes, not
// errors: the caller (a flow node) routes them.
type SystemSendStatus string

const (
	SystemQueued       SystemSendStatus = "queued"
	SystemReplayed     SystemSendStatus = "replayed"
	SystemWindowClosed SystemSendStatus = "window_closed"
	SystemNoChannel    SystemSendStatus = "no_channel"
)

// SystemSender queues outbound text on behalf of automation (the Flow runtime). It is separate from Sender on purpose:
// Sender is for operators (conversation.claim, assignee) and must keep rejecting everything that is not a human. It reuses
// the same channel/24h-window rules and the same delivery job, so the provider-facing behaviour is identical.
type SystemSender struct{ store ports.SystemOutboundStore }

func NewSystemSender(store ports.SystemOutboundStore) *SystemSender {
	return &SystemSender{store: store}
}

func (s *SystemSender) Send(ctx context.Context, conversationID uuid.UUID, text, idempotencyKey string) (SystemSendStatus, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.Source != tenancydomain.AccessSourceSystem {
		return "", ErrForbidden // only a system TenantContext may send as the system
	}
	if conversationID == uuid.Nil {
		return "", ErrNotFound
	}
	if !keyPattern.MatchString(idempotencyKey) {
		return "", ErrInvalidKey
	}
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > MaxTextRunes {
		return "", ErrInvalidText
	}
	sc, err := s.store.LoadSendContext(ctx, conversationID)
	if err != nil {
		return "", err
	}
	if sc == nil {
		return "", ErrNotFound
	}
	if sc.ConnectionID == nil || !sc.ConnectionReady || sc.ToE164 == "" {
		return SystemNoChannel, nil
	}
	if _, open := SessionWindow(sc.Provider, sc.LastInboundAt, time.Now()); !open {
		return SystemWindowClosed, nil
	}
	sum := sha256.Sum256([]byte(conversationID.String() + "\n" + text))
	hash := hex.EncodeToString(sum[:])
	msg, replayed, err := s.store.InsertQueuedSystem(ctx, *sc, text, idempotencyKey, hash)
	if err != nil {
		if errors.Is(err, ports.ErrConversationChanged) {
			// A human took the conversation or the channel changed after the checks: stay silent.
			return SystemNoChannel, nil
		}
		return "", err
	}
	if replayed {
		if msg.RequestHash != hash || msg.ConversationID != conversationID {
			return "", ErrIdempotencyMismatch
		}
		return SystemReplayed, nil
	}
	return SystemQueued, nil
}

// SendChoice queues a bot menu. On a provider that renders buttons/lists (WhatsApp Cloud API) and when every title fits
// the provider's limits, the contact gets tappable options; in every other case it is sent exactly like Send: the numbered
// text menu. text is that numbered menu, question the sentence above the options.
func (s *SystemSender) SendChoice(ctx context.Context, conversationID uuid.UUID, text, question string, options []ports.InteractiveOption, idempotencyKey string) (SystemSendStatus, error) {
	store, canInteractive := s.store.(ports.SystemInteractiveStore)
	if !canInteractive {
		return s.Send(ctx, conversationID, text, idempotencyKey)
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.Source != tenancydomain.AccessSourceSystem {
		return "", ErrForbidden
	}
	if conversationID == uuid.Nil {
		return "", ErrNotFound
	}
	if !keyPattern.MatchString(idempotencyKey) {
		return "", ErrInvalidKey
	}
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > MaxTextRunes {
		return "", ErrInvalidText
	}
	sc, err := s.store.LoadSendContext(ctx, conversationID)
	if err != nil {
		return "", err
	}
	if sc == nil {
		return "", ErrNotFound
	}
	opts := make([]channeldomain.InteractiveOption, len(options))
	for i, o := range options {
		opts[i] = channeldomain.InteractiveOption{ID: o.ID, Title: o.Title}
	}
	if sc.Provider != ProviderMetaCloud || !channeldomain.InteractiveFits(question, opts) {
		return s.Send(ctx, conversationID, text, idempotencyKey)
	}
	if sc.ConnectionID == nil || !sc.ConnectionReady || sc.ToE164 == "" {
		return SystemNoChannel, nil
	}
	if _, open := SessionWindow(sc.Provider, sc.LastInboundAt, time.Now()); !open {
		return SystemWindowClosed, nil
	}
	sum := sha256.Sum256([]byte(conversationID.String() + "\n" + text))
	hash := hex.EncodeToString(sum[:])
	msg, replayed, err := store.InsertQueuedSystemInteractive(ctx, *sc, text, idempotencyKey, hash, ports.InteractiveSend{Body: question, ListLabel: "Ver opções", Options: options})
	if err != nil {
		if errors.Is(err, ports.ErrConversationChanged) {
			return SystemNoChannel, nil
		}
		return "", err
	}
	if replayed {
		if msg.RequestHash != hash || msg.ConversationID != conversationID {
			return "", ErrIdempotencyMismatch
		}
		return SystemReplayed, nil
	}
	return SystemQueued, nil
}
