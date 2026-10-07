package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrAttachmentUnavailable: the upload does not exist for this operator in this conversation, was already sent, expired or was removed.
// One answer for all of them, so the id cannot be used to probe other tenants' or other operators' uploads.
var ErrAttachmentUnavailable = errors.New("messages: attachment is not available")

// OutboundAttachment is the record of a file an operator uploaded to send to a customer (ADR-0024). The bytes are on disk.
type OutboundAttachment struct {
	ID             uuid.UUID
	ConversationID uuid.UUID
	UploadedBy     uuid.UUID
	Kind           string // image | audio | video | document
	Mime           string // from the real bytes
	SizeBytes      int64
	SHA256         string
	FileName       string // sanitized label
	MessageID      *uuid.UUID
	ExpiresAt      time.Time
	Purged         bool
}

// Usable reports whether the attachment can still be attached to a new message by actor in conversation.
func (a OutboundAttachment) Usable(conversation, actor uuid.UUID, now time.Time) bool {
	return a.ConversationID == conversation && a.UploadedBy == actor && a.MessageID == nil && !a.Purged && a.ExpiresAt.After(now)
}

// AttachmentStore is the persistence of uploads and of the media message that carries one. Like OutboundStore it runs inside the
// request's tenant session (RLS + explicit tenant filter) and never opens its own transaction.
type AttachmentStore interface {
	InsertAttachment(ctx context.Context, a OutboundAttachment) error
	// CountPendingAttachments counts the actor's unsent, unexpired uploads in a conversation.
	CountPendingAttachments(ctx context.Context, conversationID, actor uuid.UUID) (int, error)
	// LoadAttachment returns nil when the id does not exist for the tenant (RLS hides other tenants' rows).
	LoadAttachment(ctx context.Context, id uuid.UUID) (*OutboundAttachment, error)
	// ExpireAttachment ends an unsent upload of the actor (the operator removed it): the worker deletes file and row.
	ExpireAttachment(ctx context.Context, id, actor uuid.UUID) (bool, error)
	// InsertQueuedMedia is InsertQueued for a media message: the message (type = the attachment's kind, body = the caption), its delivery
	// job and the attachment's link to the message commit together, and the link only happens if the upload is still the actor's unsent one.
	InsertQueuedMedia(ctx context.Context, sender uuid.UUID, in SendContext, att OutboundAttachment, caption, idempotencyKey, requestHash string, requireAssignee bool) (msg *QueuedMessage, replayed bool, err error)
}

// AttachmentFiles keeps the bytes of uploads outside the database.
type AttachmentFiles interface {
	Put(tenantID, id uuid.UUID, data []byte) error
	Remove(tenantID, id uuid.UUID) error
}

// VirusVerdict is the antivirus answer for a file.
type VirusVerdict struct {
	Infected  bool
	Signature string
}

// VirusScanner is the antivirus. Any error means "not scanned": the upload is refused (fail closed).
type VirusScanner interface {
	Scan(ctx context.Context, data []byte) (VirusVerdict, error)
}
