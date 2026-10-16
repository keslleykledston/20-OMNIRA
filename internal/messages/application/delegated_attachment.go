package application

import (
	"context"
	"log"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/entitlements"
	mediadomain "github.com/omnira/omnira/internal/media/domain"
	"github.com/omnira/omnira/internal/messages/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Files for a Hub agent (ADR-0040 x ADR-0024). A Hub agent is NOT a member of the conversation's company, so the member's Sender refuses them; like text
// (DelegatedSender) these run inside a system tenant session derived from a persisted row, with a guard that re-proves the delegation inside the same
// transaction, and they need the conversation to be the agent's, the line to be live and the 24 h window open. The file goes through the SAME checks as a
// member's: quotas, kind and size, metadata stripping, structure, antivirus and the company's outbound_attachments switch.

// WithDelegated gives the attachments service the sender of a Hub agent (nil: the Hub cannot send files).
func (a *Attachments) WithDelegated(d *DelegatedSender) *Attachments {
	a.delegated = d
	return a
}

// delegatedTarget runs the checks of a delegated send, without an idempotency key. guard may be nil only in tests.
func (a *Attachments) delegatedTarget(ctx context.Context, actor, conversationID uuid.UUID, guard func(ctx context.Context, sc *ports.SendContext) error) (*AttachmentTarget, *ports.SendContext, error) {
	if a.delegated == nil {
		return nil, nil, ErrMediaUnsupported
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.Source != tenancydomain.AccessSourceSystem || actor == uuid.Nil {
		return nil, nil, ErrForbidden
	}
	if conversationID == uuid.Nil {
		return nil, nil, ErrNotFound
	}
	sc, err := a.delegated.store.LoadSendContext(ctx, conversationID)
	if err != nil {
		return nil, nil, err
	}
	if sc == nil {
		return nil, nil, ErrNotFound
	}
	if guard != nil {
		if err := guard(ctx, sc); err != nil {
			return nil, nil, err
		}
	}
	if sc.Closed {
		return nil, nil, ErrConversationClosed
	}
	switch {
	case sc.AssignedTo == nil:
		return nil, nil, ErrUnassigned
	case *sc.AssignedTo != actor:
		return nil, nil, ErrNotAssignedToYou
	}
	if sc.ConnectionID == nil || !sc.ConnectionReady || sc.ToE164 == "" {
		return nil, nil, ErrChannelUnavailable
	}
	if a.sender.entitled != nil {
		if err := a.sender.entitled(ctx, tc.TenantID, entitlements.OutboundAttachments); err != nil {
			return nil, nil, err
		}
	}
	if !a.sender.mediaSupported(sc.Provider) {
		return nil, nil, ErrMediaUnsupported
	}
	if _, open := SessionWindow(sc.Provider, sc.LastInboundAt, a.sender.clock()); !open {
		return nil, nil, ErrWindowClosed
	}
	return &AttachmentTarget{TenantID: tc.TenantID, ActorID: actor, ConversationID: conversationID, Provider: sc.Provider}, sc, nil
}

// CanSendMedia says whether files may be offered to a Hub agent on this conversation right now: the company's switch, a provider this deployment delivers
// with and a live text line. It does not look at who holds the conversation (the screen shows the clip, the send refuses what is not allowed).
func (a *Attachments) CanSendMedia(ctx context.Context, tenant uuid.UUID, sc *ports.SendContext) bool {
	if a == nil || a.delegated == nil || sc == nil || sc.ConnectionID == nil || !sc.ConnectionReady || sc.ToE164 == "" {
		return false
	}
	if a.sender.entitled != nil && a.sender.entitled(ctx, tenant, entitlements.OutboundAttachments) != nil {
		return false
	}
	return a.sender.mediaSupported(sc.Provider)
}

// UploadAs validates, cleans, scans and stores one file for a Hub agent. It must run in a system tenant session; guard re-proves the delegation.
func (a *Attachments) UploadAs(ctx context.Context, actor, conversationID uuid.UUID, declaredName, declaredMime string, data []byte,
	guard func(ctx context.Context, sc *ports.SendContext) error) (*ports.OutboundAttachment, error) {
	target, _, err := a.delegatedTarget(ctx, actor, conversationID, guard)
	if err != nil {
		return nil, err
	}
	return a.ingest(ctx, target, conversationID, declaredName, declaredMime, data)
}

// RemoveAs drops an unsent upload of the Hub agent (they took the file out of the composer).
func (a *Attachments) RemoveAs(ctx context.Context, actor, conversationID, attachmentID uuid.UUID) error {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.Source != tenancydomain.AccessSourceSystem || actor == uuid.Nil {
		return ErrForbidden
	}
	att, err := a.store.LoadAttachment(ctx, attachmentID)
	if err != nil {
		return err
	}
	if att == nil || att.ConversationID != conversationID || att.UploadedBy != actor || att.MessageID != nil {
		return ErrAttachmentUnavailable
	}
	ok, err := a.store.ExpireAttachment(ctx, attachmentID, actor)
	if err != nil {
		return err
	}
	if !ok {
		return ErrAttachmentUnavailable
	}
	return nil
}

// SendMediaAs queues a media message of a Hub agent that carries a previously uploaded file. caption may be empty.
func (a *Attachments) SendMediaAs(ctx context.Context, actor, conversationID, attachmentID uuid.UUID, caption, idempotencyKey string,
	guard func(ctx context.Context, sc *ports.SendContext) error) (SendResult, error) {
	if !mediadomain.CaptionValid(caption) {
		return SendResult{}, ErrInvalidCaption
	}
	if !keyPattern.MatchString(idempotencyKey) {
		return SendResult{}, ErrInvalidKey
	}
	target, sc, err := a.delegatedTarget(ctx, actor, conversationID, guard)
	if err != nil {
		return SendResult{}, err
	}
	att, err := a.store.LoadAttachment(ctx, attachmentID)
	if err != nil {
		return SendResult{}, err
	}
	hash := mediaRequestHash(conversationID, attachmentID, caption)
	if att == nil || !att.Usable(conversationID, actor, a.sender.clock()) {
		// a replay of the same request finds the attachment already attached to ITS message: let the store resolve that
		if att != nil && att.UploadedBy == actor && att.ConversationID == conversationID && att.MessageID != nil {
			msg, replayed, err := a.store.InsertQueuedMedia(ctx, actor, *sc, *att, caption, idempotencyKey, hash, true)
			if err == nil && replayed && msg.RequestHash == hash && msg.ConversationID == conversationID {
				return SendResult{Message: msg, Replayed: true}, nil
			}
		}
		return SendResult{}, ErrAttachmentUnavailable
	}
	if !mediadomain.OutboundMimeAllowed(sc.Provider, att.Mime) {
		return SendResult{}, &AttachmentRejected{Reason: mediadomain.ReasonUnsupportedForChannel}
	}
	msg, replayed, err := a.store.InsertQueuedMedia(ctx, actor, *sc, *att, caption, idempotencyKey, hash, true)
	if err != nil {
		return SendResult{}, err
	}
	if replayed && (msg.RequestHash != hash || msg.ConversationID != conversationID) {
		return SendResult{}, ErrIdempotencyMismatch
	}
	if !replayed {
		log.Printf("messages: accepted media message_id=%s tenant_id=%s conversation_id=%s kind=%s outcome=queued via=hub", msg.ID, target.TenantID, conversationID, att.Kind)
	}
	return SendResult{Message: msg, Replayed: replayed}, nil
}
