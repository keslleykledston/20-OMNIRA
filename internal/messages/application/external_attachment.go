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

// Files for an external application (ADR-0048 x ADR-0024). The application is neither a member nor a Hub agent: like text (ExternalSender) these run in a system
// tenant session after the external write path re-checked the installation, the scope and the visibility. The file goes through the SAME pipeline as an
// operator's (quotas, kind and size, metadata stripping, structure, antivirus, the company's outbound_attachments switch); the record names the installation as its
// owner, and the message carries it only if the installation still holds the conversation at the control version it proved, in the very statement that queues it.

// WithExternal gives the attachments service the store of an external application's files (nil: applications cannot send files).
func (a *Attachments) WithExternal(files ports.ExternalAttachmentStore, out ports.ExternalOutboundStore) *Attachments {
	a.external, a.externalOut = files, out
	return a
}

// externalTarget runs the checks of an application's file send: the service is wired, the company's switch is on and
// the provider is one this deployment delivers media with.
func (a *Attachments) externalTarget(ctx context.Context, installation, conversationID uuid.UUID, sc *ports.SendContext) (*AttachmentTarget, error) {
	if a == nil || a.external == nil {
		return nil, ErrMediaUnsupported
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.Source != tenancydomain.AccessSourceSystem || installation == uuid.Nil {
		return nil, ErrForbidden // only the external write path, in a system session
	}
	if conversationID == uuid.Nil || sc == nil {
		return nil, ErrNotFound
	}
	if sc.Closed {
		return nil, ErrConversationClosed
	}
	if sc.ConnectionID == nil || !sc.ConnectionReady || sc.ToE164 == "" {
		return nil, ErrChannelUnavailable
	}
	if a.sender.entitled != nil {
		if err := a.sender.entitled(ctx, tc.TenantID, entitlements.OutboundAttachments); err != nil {
			return nil, err
		}
	}
	if !a.sender.mediaSupported(sc.Provider) {
		return nil, ErrMediaUnsupported
	}
	if _, open := SessionWindow(sc.Provider, sc.LastInboundAt, a.sender.clock()); !open {
		return nil, ErrWindowClosed
	}
	return &AttachmentTarget{TenantID: tc.TenantID, ConversationID: conversationID, Provider: sc.Provider, InstallationID: installation}, nil
}

// UploadExternal validates, cleans, scans and stores one file for an application. The caller has proved, in this session, that the installation may see the
// conversation and holds it now; the send proves it again at the version it presents.
func (a *Attachments) UploadExternal(ctx context.Context, installation, conversationID uuid.UUID, declaredName, declaredMime string, data []byte) (*ports.OutboundAttachment, error) {
	if a == nil || a.externalOut == nil {
		return nil, ErrMediaUnsupported
	}
	sc, err := a.externalOut.LoadSendContext(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	target, err := a.externalTarget(ctx, installation, conversationID, sc)
	if err != nil {
		return nil, err
	}
	return a.ingest(ctx, target, conversationID, declaredName, declaredMime, data)
}

// ExternalMediaMessage is one media message to queue for an application. Caption may be empty.
type ExternalMediaMessage struct {
	InstallationID uuid.UUID
	ConversationID uuid.UUID
	ControlVersion int64
	AttachmentID   uuid.UUID
	Caption        string
}

// SendMediaExternal queues the media message that carries a file the application uploaded. It never talks to a provider (202 means "queued").
func (a *Attachments) SendMediaExternal(ctx context.Context, m ExternalMediaMessage) (*ports.QueuedMessage, error) {
	if a == nil || a.externalOut == nil {
		return nil, ErrMediaUnsupported
	}
	if !mediadomain.CaptionValid(m.Caption) {
		return nil, ErrInvalidCaption
	}
	sc, err := a.externalOut.LoadSendContext(ctx, m.ConversationID)
	if err != nil {
		return nil, err
	}
	if _, err := a.externalTarget(ctx, m.InstallationID, m.ConversationID, sc); err != nil {
		return nil, err
	}
	att, err := a.store.LoadAttachment(ctx, m.AttachmentID)
	if err != nil {
		return nil, err
	}
	if att == nil || !att.UsableByInstallation(m.ConversationID, m.InstallationID, a.sender.clock()) {
		return nil, ErrAttachmentUnavailable // one answer for every reason, so the id cannot probe other installations' or tenants' uploads
	}
	// the line may have changed since the upload (a WebP is fine on WAHA and not on Meta)
	if !mediadomain.OutboundMimeAllowed(sc.Provider, att.Mime) {
		return nil, &AttachmentRejected{Reason: mediadomain.ReasonUnsupportedForChannel}
	}
	msg, err := a.external.InsertQueuedExternalMedia(ctx, m.InstallationID, m.ControlVersion, *sc, *att, m.Caption, mediaRequestHash(m.ConversationID, m.AttachmentID, m.Caption))
	if err != nil {
		return nil, err
	}
	tc, _ := tenancydomain.FromContext(ctx)
	log.Printf("messages: accepted external media message_id=%s tenant_id=%s conversation_id=%s kind=%s outcome=queued", msg.ID, tc.TenantID, m.ConversationID, att.Kind)
	return msg, nil
}
