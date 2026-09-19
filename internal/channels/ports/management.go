package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
)

// SessionStatus is the provider-neutral session state used by connection
// management. Adapters translate their vocabulary into these values.
type SessionStatus string

const (
	SessionMissing  SessionStatus = "missing"  // provider has no session for this connection yet
	SessionStarting SessionStatus = "starting" // booting
	SessionNeedsQR  SessionStatus = "needs_qr" // waiting for the operator to scan
	SessionWorking  SessionStatus = "working"  // paired and online
	SessionStopped  SessionStatus = "stopped"
	SessionFailed   SessionStatus = "failed"
)

// QRImage is a pairing QR code ready to render (base64 data, never logged).
type QRImage struct {
	MIMEType string
	Data     string
}

// SessionController operates one provider session bound to one connection.
// The session name is derived from the connection id by the adapter; callers
// never supply it. Authorization happens before any call.
type SessionController interface {
	Status(ctx context.Context, conn domain.ChannelConnection) (SessionStatus, error)
	// Create registers the session with this connection's callback webhook. The
	// HMAC key is resolved server-side from the encrypted credential (never a parameter).
	Create(ctx context.Context, conn domain.ChannelConnection, webhookBaseURL string) error
	Start(ctx context.Context, conn domain.ChannelConnection) error
	Stop(ctx context.Context, conn domain.ChannelConnection) error
	QR(ctx context.Context, conn domain.ChannelConnection) (QRImage, error)
	// Account returns the paired account identifier (digits), if any.
	Account(ctx context.Context, conn domain.ChannelConnection) (string, error)
}

// PermissionChecker resolves the actor's role permissions in the TenantContext tenant.
type PermissionChecker interface {
	HasPermission(ctx context.Context, userID uuid.UUID, permission string) (bool, error)
}

// ChannelAudit appends channel management audit events in the caller's transaction.
type ChannelAudit interface {
	Record(ctx context.Context, action, resourceType string, resourceID uuid.UUID, meta map[string]any) error
}
