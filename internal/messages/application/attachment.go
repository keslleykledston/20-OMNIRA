package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/omnira/omnira/internal/entitlements"
	"log"
	"time"

	"github.com/google/uuid"
	mediadomain "github.com/omnira/omnira/internal/media/domain"
	"github.com/omnira/omnira/internal/messages/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Outbound attachments (ADR-0024). Two steps on purpose: the operator UPLOADS a file (validated, cleared by the antivirus, stored) and then
// SENDS a message that references it. Everything dangerous happens in the first step, where the answer can still be "no", and the second step
// is the same authorize-queue-deliver path as text.

const (
	// MaxPendingAttachments bounds the unsent uploads one operator can hold in one conversation.
	MaxPendingAttachments = 5
	// AttachmentTTL is how long an unsent upload is kept.
	AttachmentTTL = 24 * time.Hour
)

var (
	ErrMediaUnsupported      = errors.New("messages: this channel cannot send media")
	ErrInvalidCaption        = errors.New("messages: caption is limited to 1024 characters of plain text")
	ErrAttachmentUnavailable = ports.ErrAttachmentUnavailable
	ErrTooManyAttachments    = errors.New("messages: too many unsent attachments in this conversation")
	ErrAttachmentInfected    = errors.New("messages: the file was blocked by the antivirus")
	ErrScannerUnavailable    = errors.New("messages: the antivirus is unavailable, the file was not accepted")
	ErrAttachmentQuota       = errors.New("messages: the attachment storage quota was reached")
	ErrAttachmentRate        = errors.New("messages: too many uploads, wait a moment")
)

const (
	// MaxPendingBytes bounds the unsent uploads of one operator (5 files of 16 MiB at most would be 80 MiB).
	MaxPendingBytes = 64 << 20
	// MaxTenantBytes bounds the files a tenant keeps (unsent, plus sent ones inside the 60-day retention).
	MaxTenantBytes = 2 << 30
	// MaxUploadsPerMinute is the durable (database-counted, so it holds across replicas) per-operator upload rate.
	MaxUploadsPerMinute = 30
)

// decodeSlots bounds the full image decodes running at once: a 12-megapixel decode allocates ~50 MiB.
var decodeSlots = make(chan struct{}, 2)

// AttachmentRejected carries the stable reason of a refused file (media/domain Reason* constants).
type AttachmentRejected struct{ Reason string }

func (e *AttachmentRejected) Error() string { return "messages: attachment rejected: " + e.Reason }

// Attachments uploads files for outbound messages.
type Attachments struct {
	sender  *Sender
	store   ports.AttachmentStore
	files   ports.AttachmentFiles
	scanner ports.VirusScanner
	now     func() time.Time
}

func NewAttachments(sender *Sender, store ports.AttachmentStore, files ports.AttachmentFiles, scanner ports.VirusScanner) *Attachments {
	return &Attachments{sender: sender, store: store, files: files, scanner: scanner, now: time.Now}
}

// AttachmentTarget is the conversation an upload is for, after the authorization checks of a send.
type AttachmentTarget struct {
	TenantID, ActorID, ConversationID uuid.UUID
	Provider                          string
}

func (s *Sender) mediaSupported(provider string) bool {
	return mediadomain.OutboundMediaSupported(provider) && (s.mediaReady == nil || s.mediaReady(provider))
}

// authorizeUpload runs the checks of a send (permission, assignment, open conversation, active channel, 24 h window) without an
// idempotency key: there is no point accepting a file the operator could not send.
func (s *Sender) authorizeUpload(ctx context.Context, conversationID uuid.UUID) (*AttachmentTarget, error) {
	p, err := s.authorizeConversation(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if !s.mediaSupported(p.sc.Provider) {
		return nil, ErrMediaUnsupported
	}
	if _, open := SessionWindow(p.sc.Provider, p.sc.LastInboundAt, s.clock()); !open {
		return nil, ErrWindowClosed
	}
	return &AttachmentTarget{TenantID: p.tc.TenantID, ActorID: p.tc.ActorID, ConversationID: conversationID, Provider: p.sc.Provider}, nil
}

// Upload validates, cleans, scans and stores one file and returns its record.
func (a *Attachments) Upload(ctx context.Context, conversationID uuid.UUID, declaredName, declaredMime string, data []byte) (*ports.OutboundAttachment, error) {
	target, err := a.sender.authorizeUpload(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	usage, err := a.store.AttachmentUsage(ctx, conversationID, target.ActorID)
	if err != nil {
		return nil, err
	}
	switch {
	case usage.PendingFiles >= MaxPendingAttachments:
		return nil, ErrTooManyAttachments
	case usage.UploadsLastMinute >= MaxUploadsPerMinute:
		return nil, ErrAttachmentRate
	case usage.PendingBytes+int64(len(data)) > MaxPendingBytes || usage.TenantBytes+int64(len(data)) > MaxTenantBytes:
		return nil, ErrAttachmentQuota
	}
	res, err := mediadomain.ClassifyOutbound(data, declaredMime, target.Provider)
	if err != nil {
		if reason, ok := mediadomain.IsRejection(err); ok {
			return nil, &AttachmentRejected{Reason: reason}
		}
		return nil, err
	}
	clean, err := mediadomain.StripMetadata(data, res.Mime)
	if err != nil {
		if reason, ok := mediadomain.IsRejection(err); ok {
			return nil, &AttachmentRejected{Reason: reason}
		}
		return nil, err
	}
	if len(clean) != len(data) {
		// what is stored and sent is the stripped file: classify it again, it must still be the same kind of thing
		if again, err := mediadomain.ClassifyOutbound(clean, "", target.Provider); err != nil || again.Mime != res.Mime {
			return nil, &AttachmentRejected{Reason: mediadomain.ReasonImageUnreadable}
		}
	}
	select {
	case decodeSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	structureErr := mediadomain.ValidateStructure(clean, res.Mime)
	<-decodeSlots
	if err := structureErr; err != nil {
		if reason, ok := mediadomain.IsRejection(err); ok {
			return nil, &AttachmentRejected{Reason: reason}
		}
		return nil, err
	}
	verdict, err := a.scanner.Scan(ctx, clean)
	if err != nil {
		log.Printf("messages: antivirus unavailable for an upload tenant_id=%s: %v", target.TenantID, err)
		return nil, ErrScannerUnavailable
	}
	if verdict.Infected {
		log.Printf("messages: upload blocked by the antivirus tenant_id=%s conversation_id=%s signature=%q", target.TenantID, conversationID, verdict.Signature)
		return nil, ErrAttachmentInfected
	}
	sum := sha256.Sum256(clean)
	att := ports.OutboundAttachment{
		ID: uuid.New(), ConversationID: conversationID, UploadedBy: target.ActorID,
		Kind: string(res.Kind), Mime: res.Mime, SizeBytes: int64(len(clean)), SHA256: hex.EncodeToString(sum[:]),
		FileName: mediadomain.SafeFileName(declaredName, res.Mime), ExpiresAt: a.now().Add(AttachmentTTL),
	}
	if err := a.files.Put(target.TenantID, att.ID, clean); err != nil {
		return nil, fmt.Errorf("messages: store attachment: %w", err)
	}
	if err := a.store.InsertAttachment(ctx, att); err != nil {
		_ = a.files.Remove(target.TenantID, att.ID)
		return nil, err
	}
	log.Printf("messages: attachment accepted attachment_id=%s tenant_id=%s conversation_id=%s kind=%s bytes=%d", att.ID, target.TenantID, conversationID, att.Kind, att.SizeBytes)
	return &att, nil
}

// Remove drops an unsent upload of the caller (the operator took the file out of the composer).
func (a *Attachments) Remove(ctx context.Context, conversationID, attachmentID uuid.UUID) error {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil || tc.Source != tenancydomain.AccessSourceDirect {
		return ErrForbidden
	}
	att, err := a.store.LoadAttachment(ctx, attachmentID)
	if err != nil {
		return err
	}
	if att == nil || att.ConversationID != conversationID || att.UploadedBy != tc.ActorID || att.MessageID != nil {
		return ErrAttachmentUnavailable
	}
	ok, err := a.store.ExpireAttachment(ctx, attachmentID, tc.ActorID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrAttachmentUnavailable
	}
	return nil
}

// SendMedia queues a media message that carries a previously uploaded file. caption may be empty.
func (s *Sender) SendMedia(ctx context.Context, store ports.AttachmentStore, conversationID, attachmentID uuid.UUID, caption, idempotencyKey string) (SendResult, error) {
	if !mediadomain.CaptionValid(caption) {
		if conversationID == uuid.Nil {
			return SendResult{}, ErrNotFound
		}
		if tc, err := tenancydomain.FromContext(ctx); err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil || tc.Source != tenancydomain.AccessSourceDirect {
			return SendResult{}, ErrForbidden
		}
		if !keyPattern.MatchString(idempotencyKey) {
			return SendResult{}, ErrInvalidKey
		}
		return SendResult{}, ErrInvalidCaption
	}
	p, err := s.authorize(ctx, conversationID, idempotencyKey)
	if err != nil {
		return SendResult{}, err
	}
	tc, sc, manage := p.tc, p.sc, p.manage
	if s.entitled != nil {
		if err := s.entitled(ctx, tc.TenantID, entitlements.OutboundAttachments); err != nil {
			return SendResult{}, err
		}
	}
	if !s.mediaSupported(sc.Provider) {
		return SendResult{}, ErrMediaUnsupported
	}
	if _, open := SessionWindow(sc.Provider, sc.LastInboundAt, s.clock()); !open {
		return SendResult{}, ErrWindowClosed
	}
	att, err := store.LoadAttachment(ctx, attachmentID)
	if err != nil {
		return SendResult{}, err
	}
	hash := mediaRequestHash(conversationID, attachmentID, caption)
	if att == nil || !att.Usable(conversationID, tc.ActorID, s.clock()) {
		// A replay of the same request finds the attachment already attached to ITS message: let the store resolve that.
		if att != nil && att.UploadedBy == tc.ActorID && att.ConversationID == conversationID && att.MessageID != nil {
			msg, replayed, err := store.InsertQueuedMedia(ctx, tc.ActorID, *sc, *att, caption, idempotencyKey, hash, !manage)
			if err == nil && replayed && msg.RequestHash == hash && msg.ConversationID == conversationID {
				return SendResult{Message: msg, Replayed: true}, nil
			}
		}
		return SendResult{}, ErrAttachmentUnavailable
	}
	// The connection may have changed since the upload (a WebP is fine on WAHA and not on Meta).
	if !mediadomain.OutboundMimeAllowed(sc.Provider, att.Mime) {
		return SendResult{}, &AttachmentRejected{Reason: mediadomain.ReasonUnsupportedForChannel}
	}
	msg, replayed, err := store.InsertQueuedMedia(ctx, tc.ActorID, *sc, *att, caption, idempotencyKey, hash, !manage)
	if err != nil {
		return SendResult{}, err
	}
	if replayed && (msg.RequestHash != hash || msg.ConversationID != conversationID) {
		return SendResult{}, ErrIdempotencyMismatch
	}
	if !replayed {
		log.Printf("messages: accepted media message_id=%s tenant_id=%s conversation_id=%s kind=%s outcome=queued", msg.ID, tc.TenantID, conversationID, att.Kind)
	}
	return SendResult{Message: msg, Replayed: replayed}, nil
}

func mediaRequestHash(conversationID, attachmentID uuid.UUID, caption string) string {
	sum := sha256.Sum256([]byte(conversationID.String() + "\nmedia:" + attachmentID.String() + "\n" + caption))
	return hex.EncodeToString(sum[:])
}
