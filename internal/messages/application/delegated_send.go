package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/messages/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// DelegatedSender queues outbound text for an actor who is NOT a member of the conversation's tenant: a Hub agent
// answering through a live service grant. It is separate from Sender on purpose: Sender's contract is "a direct member
// with conversation.claim" and keeps rejecting everything else.
//
// It must run inside a system tenant session (platformdb.WithSystemTenantSession) whose tenant was derived from a
// persisted row, never from the request. The system session is only the means to write; WHO may write is decided by
// the caller before and by guard inside the same transaction:
//
//   - guard re-validates the delegation (grant, contract, queue scope, reply capability) after the conversation is
//     loaded and before anything is inserted, so a revocation that commits first is honoured.
//   - the conversation must be assigned to the actor (no manager bypass: a delegated agent has no tenant role), and
//     InsertQueued re-checks that atomically with FOR SHARE.
//   - same channel / 24 h window / delivery job as every other outbound text.
type DelegatedSender struct {
	store ports.OutboundStore
	now   func() time.Time
}

func NewDelegatedSender(store ports.OutboundStore) *DelegatedSender {
	return &DelegatedSender{store: store, now: time.Now}
}

// Send queues text from actor in the conversation. guard may be nil only in tests.
func (d *DelegatedSender) Send(ctx context.Context, actor, conversationID uuid.UUID, text, idempotencyKey string,
	guard func(ctx context.Context, sc *ports.SendContext) error) (SendResult, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.Source != tenancydomain.AccessSourceSystem || actor == uuid.Nil {
		return SendResult{}, ErrForbidden
	}
	if conversationID == uuid.Nil {
		return SendResult{}, ErrNotFound
	}
	if !keyPattern.MatchString(idempotencyKey) {
		return SendResult{}, ErrInvalidKey
	}
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > MaxTextRunes {
		return SendResult{}, ErrInvalidText
	}
	sc, err := d.store.LoadSendContext(ctx, conversationID)
	if err != nil {
		return SendResult{}, err
	}
	if sc == nil {
		return SendResult{}, ErrNotFound
	}
	if guard != nil {
		if err := guard(ctx, sc); err != nil {
			return SendResult{}, err
		}
	}
	if sc.Closed {
		return SendResult{}, ErrConversationClosed
	}
	switch {
	case sc.AssignedTo == nil:
		return SendResult{}, ErrUnassigned
	case *sc.AssignedTo != actor:
		return SendResult{}, ErrNotAssignedToYou
	}
	if sc.ConnectionID == nil || !sc.ConnectionReady || sc.ToE164 == "" {
		return SendResult{}, ErrChannelUnavailable
	}
	if _, open := SessionWindow(sc.Provider, sc.LastInboundAt, d.clock()); !open {
		return SendResult{}, ErrWindowClosed
	}
	sum := sha256.Sum256([]byte(conversationID.String() + "\n" + text))
	hash := hex.EncodeToString(sum[:])
	msg, replayed, err := d.store.InsertQueued(ctx, actor, *sc, text, idempotencyKey, hash, true)
	if err != nil {
		return SendResult{}, err
	}
	if replayed && (msg.RequestHash != hash || msg.ConversationID != conversationID) {
		return SendResult{}, ErrIdempotencyMismatch
	}
	if !replayed {
		log.Printf("messages: accepted message_id=%s tenant_id=%s conversation_id=%s outcome=queued via=hub", msg.ID, tc.TenantID, conversationID)
	}
	return SendResult{Message: msg, Replayed: replayed}, nil
}

func (d *DelegatedSender) clock() time.Time {
	if d.now == nil {
		return time.Now()
	}
	return d.now()
}
