package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/messages/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

const (
	PermissionClaim  = "conversation.claim"
	PermissionManage = "conversation.manage"
	MaxTextRunes     = 4096
)

var (
	ErrForbidden           = errors.New("messages: forbidden")
	ErrNotFound            = errors.New("messages: conversation not found")
	ErrNotAssignedToYou    = errors.New("messages: conversation is assigned to another agent")
	ErrUnassigned          = errors.New("messages: conversation must be assigned before replying")
	ErrConversationClosed  = errors.New("messages: the conversation was finalized; the contact's next message starts a new attendance")
	ErrChannelUnavailable  = errors.New("messages: conversation has no active text channel")
	ErrTemplateUnsupported = errors.New("messages: this channel does not use message templates")
	ErrTemplateNotAllowed  = errors.New("messages: template is not available for this conversation's channel (not approved, not supported or from another line)")
	ErrTemplateParams      = errors.New("messages: the template variables are missing, empty, too long or contain line breaks")
	ErrWindowClosed        = errors.New("messages: the 24 h customer-service window is closed, a template is required")
	ErrInvalidText         = errors.New("messages: text is required (max 4096 characters)")
	ErrInvalidKey          = errors.New("messages: Idempotency-Key must be 8-128 chars of [A-Za-z0-9._:-]")
	ErrConversationChanged = ports.ErrConversationChanged
	ErrIdempotencyMismatch = errors.New("messages: Idempotency-Key was already used with a different request")
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

// SendResult is the queued message plus whether it was an idempotent replay.
type SendResult struct {
	Message  *ports.QueuedMessage
	Replayed bool
}

// Sender queues outbound text. It never talks to a provider: the message and
// its delivery job are persisted atomically and delivered by the worker.
type Sender struct {
	store ports.OutboundStore
	perms ports.PermissionChecker
	now   func() time.Time
}

func NewSender(store ports.OutboundStore, perms ports.PermissionChecker) *Sender {
	return &Sender{store: store, perms: perms, now: time.Now}
}

func (s *Sender) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

func (s *Sender) has(ctx context.Context, user uuid.UUID, permission string) (bool, error) {
	return s.perms.HasPermission(ctx, user, permission)
}

// prepared is the outcome of the checks every outbound send shares: who may send in which conversation, over which line.
type prepared struct {
	tc     *tenancydomain.TenantContext
	sc     *ports.SendContext
	manage bool
}

// authorize runs the checks common to text and template sends. Order matters and is the same as before: shape of the
// request first (the caller validates the payload), then permission, then the conversation's own state.
func (s *Sender) authorize(ctx context.Context, conversationID uuid.UUID, idempotencyKey string) (*prepared, error) {
	if conversationID == uuid.Nil {
		return nil, ErrNotFound
	}
	if tc, err := tenancydomain.FromContext(ctx); err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil || tc.Source != tenancydomain.AccessSourceDirect {
		return nil, ErrForbidden
	}
	if !keyPattern.MatchString(idempotencyKey) {
		return nil, ErrInvalidKey
	}
	return s.authorizeConversation(ctx, conversationID)
}

// authorizeConversation is the part of authorize that does not depend on an idempotency key: who may send in which conversation.
func (s *Sender) authorizeConversation(ctx context.Context, conversationID uuid.UUID) (*prepared, error) {
	if conversationID == uuid.Nil {
		return nil, ErrNotFound
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil || tc.Source != tenancydomain.AccessSourceDirect {
		return nil, ErrForbidden
	}
	ok, err := s.has(ctx, tc.ActorID, PermissionClaim)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrForbidden
	}
	sc, err := s.store.LoadSendContext(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if sc == nil {
		return nil, ErrNotFound
	}
	if sc.Closed {
		return nil, ErrConversationClosed
	}
	manage, err := s.has(ctx, tc.ActorID, PermissionManage)
	if err != nil {
		return nil, err
	}
	switch {
	case sc.AssignedTo == nil:
		return nil, ErrUnassigned
	case *sc.AssignedTo != tc.ActorID && !manage:
		return nil, ErrNotAssignedToYou
	}
	if sc.ConnectionID == nil || !sc.ConnectionReady || sc.ToE164 == "" {
		return nil, ErrChannelUnavailable
	}
	return &prepared{tc: tc, sc: sc, manage: manage}, nil
}

func (s *Sender) Send(ctx context.Context, conversationID uuid.UUID, text, idempotencyKey string) (SendResult, error) {
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > MaxTextRunes {
		// payload shape is judged before anything about the conversation is revealed, as before
		if conversationID == uuid.Nil {
			return SendResult{}, ErrNotFound
		}
		if tc, err := tenancydomain.FromContext(ctx); err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil || tc.Source != tenancydomain.AccessSourceDirect {
			return SendResult{}, ErrForbidden
		}
		if !keyPattern.MatchString(idempotencyKey) {
			return SendResult{}, ErrInvalidKey
		}
		return SendResult{}, ErrInvalidText
	}
	p, err := s.authorize(ctx, conversationID, idempotencyKey)
	if err != nil {
		return SendResult{}, err
	}
	tc, sc, manage := p.tc, p.sc, p.manage
	if _, open := SessionWindow(sc.Provider, sc.LastInboundAt, time.Now()); !open {
		return SendResult{}, ErrWindowClosed
	}
	sum := sha256.Sum256([]byte(conversationID.String() + "\n" + text))
	hash := hex.EncodeToString(sum[:])
	// A non-manager must still be the assignee at insert time (re-checked atomically by the store).
	msg, replayed, err := s.store.InsertQueued(ctx, tc.ActorID, *sc, text, idempotencyKey, hash, !manage)
	if err != nil {
		return SendResult{}, err
	}
	if replayed && (msg.RequestHash != hash || msg.ConversationID != conversationID) {
		return SendResult{}, ErrIdempotencyMismatch
	}
	if !replayed {
		// PILOT.4B: the first observable point in the outbound lifecycle,
		// logged only after InsertQueued's atomic transaction has committed.
		// message_id is the durable cross-stage correlation key from here on
		// (through outbox publish and worker delivery) — no message text.
		log.Printf("messages: accepted message_id=%s tenant_id=%s conversation_id=%s outcome=queued", msg.ID, tc.TenantID, conversationID)
	}
	return SendResult{Message: msg, Replayed: replayed}, nil
}

// SendTemplate queues an approved WhatsApp template (allowed inside or outside the 24 h window, which is how a
// conversation starts with someone who has not written to this number). Same permission and assignment rules as
// Send. The inbox shows the rendered text; the provider receives the template name, language and variables.
func (s *Sender) SendTemplate(ctx context.Context, conversationID, templateID uuid.UUID, params []string, idempotencyKey string) (SendResult, error) {
	p, err := s.authorize(ctx, conversationID, idempotencyKey)
	if err != nil {
		return SendResult{}, err
	}
	tc, sc, manage := p.tc, p.sc, p.manage
	if sc.Provider != ProviderMetaCloud {
		return SendResult{}, ErrTemplateUnsupported
	}
	tpl, err := s.store.LoadTemplate(ctx, *sc.ConnectionID, templateID)
	if err != nil {
		return SendResult{}, err
	}
	if tpl == nil || tpl.Status != "APPROVED" || !tpl.Sendable {
		return SendResult{}, ErrTemplateNotAllowed
	}
	body, err := RenderTemplate(tpl.Body, tpl.VariableCount, params)
	if err != nil {
		return SendResult{}, err
	}
	sum := sha256.Sum256([]byte(conversationID.String() + "\ntpl:" + templateID.String() + "\n" + strings.Join(params, "\x1f")))
	hash := hex.EncodeToString(sum[:])
	msg, replayed, err := s.store.InsertQueuedTemplate(ctx, tc.ActorID, *sc, body, idempotencyKey, hash, !manage,
		ports.TemplateSend{Name: tpl.Name, Language: tpl.Language, Params: params})
	if err != nil {
		return SendResult{}, err
	}
	if replayed && (msg.RequestHash != hash || msg.ConversationID != conversationID) {
		return SendResult{}, ErrIdempotencyMismatch
	}
	if !replayed {
		log.Printf("messages: accepted template message_id=%s tenant_id=%s conversation_id=%s outcome=queued", msg.ID, tc.TenantID, conversationID)
	}
	return SendResult{Message: msg, Replayed: replayed}, nil
}
