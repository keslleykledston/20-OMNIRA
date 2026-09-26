package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"regexp"
	"strings"
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
	ErrChannelUnavailable  = errors.New("messages: conversation has no active text channel")
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
}

func NewSender(store ports.OutboundStore, perms ports.PermissionChecker) *Sender {
	return &Sender{store: store, perms: perms}
}

func (s *Sender) has(ctx context.Context, user uuid.UUID, permission string) (bool, error) {
	return s.perms.HasPermission(ctx, user, permission)
}

func (s *Sender) Send(ctx context.Context, conversationID uuid.UUID, text, idempotencyKey string) (SendResult, error) {
	if conversationID == uuid.Nil {
		return SendResult{}, ErrNotFound
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil || tc.Source != tenancydomain.AccessSourceDirect {
		return SendResult{}, ErrForbidden
	}
	if !keyPattern.MatchString(idempotencyKey) {
		return SendResult{}, ErrInvalidKey
	}
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > MaxTextRunes {
		return SendResult{}, ErrInvalidText
	}
	ok, err := s.has(ctx, tc.ActorID, PermissionClaim)
	if err != nil {
		return SendResult{}, err
	}
	if !ok {
		return SendResult{}, ErrForbidden
	}
	sc, err := s.store.LoadSendContext(ctx, conversationID)
	if err != nil {
		return SendResult{}, err
	}
	if sc == nil {
		return SendResult{}, ErrNotFound
	}
	manage, err := s.has(ctx, tc.ActorID, PermissionManage)
	if err != nil {
		return SendResult{}, err
	}
	switch {
	case sc.AssignedTo == nil:
		return SendResult{}, ErrUnassigned
	case *sc.AssignedTo != tc.ActorID && !manage:
		return SendResult{}, ErrNotAssignedToYou
	}
	if sc.ConnectionID == nil || !sc.ConnectionReady || sc.ToE164 == "" {
		return SendResult{}, ErrChannelUnavailable
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
