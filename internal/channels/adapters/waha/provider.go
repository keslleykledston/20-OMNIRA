package waha

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
)

var (
	ErrInvalidConnection = errors.New("waha: invalid channel connection")
	ErrSessionOwnership  = errors.New("waha: provider session reference does not belong to connection")
)

// WahaProvider owns WAHA-specific session lifecycle. Authorization remains
// outside this adapter: callers must resolve and authorize conn first.
type WahaProvider struct {
	client      *Client
	credentials ports.CredentialStore
}

var _ ports.ChannelProvider = (*WahaProvider)(nil)

func NewProvider(client *Client, stores ...ports.CredentialStore) (*WahaProvider, error) {
	if client == nil {
		return nil, ErrConfiguration
	}
	var credentials ports.CredentialStore
	if len(stores) > 0 {
		credentials = stores[0]
	}
	return &WahaProvider{client: client, credentials: credentials}, nil
}

// SessionRef deterministically binds one WAHA session to one connection.
// User-provided session names are never accepted.
func (p *WahaProvider) SessionRef(conn domain.ChannelConnection) (string, error) {
	if err := validateConnection(conn); err != nil {
		return "", err
	}
	expected := "omnira_" + conn.ID.String()
	if conn.ProviderSessionRef != "" && conn.ProviderSessionRef != expected {
		return "", ErrSessionOwnership
	}
	return expected, nil
}

func (p *WahaProvider) CreateSession(ctx context.Context, conn domain.ChannelConnection) (Session, error) {
	name, err := p.SessionRef(conn)
	if err != nil {
		return Session{}, err
	}
	return p.client.CreateSession(ctx, name)
}

// CreateSessionWithWebhook configures only this connection's webhook. The
// HMAC key is resolved server-side and sent only to WAHA over the adapter
// request; it is never returned to the caller.
func (p *WahaProvider) CreateSessionWithWebhook(ctx context.Context, conn domain.ChannelConnection, publicWebhookBaseURL string) (Session, error) {
	name, err := p.SessionRef(conn)
	if err != nil {
		return Session{}, err
	}
	if p.credentials == nil || conn.SecretRef == "" {
		return Session{}, ports.ErrNotConfigured
	}
	credential, err := p.credentials.Resolve(ctx, conn.SecretRef)
	if err != nil {
		return Session{}, errors.New("waha: resolve webhook credential failed")
	}
	hmacKey := credential.Fields["webhook_hmac_key"]
	if hmacKey == "" || strings.TrimSpace(publicWebhookBaseURL) == "" {
		return Session{}, ports.ErrNotConfigured
	}
	callback := strings.TrimRight(publicWebhookBaseURL, "/") + "/webhooks/v1/whatsapp/waha/" + conn.ID.String()
	return p.client.CreateSessionWithWebhook(ctx, name, callback, hmacKey)
}

func (p *WahaProvider) StartSession(ctx context.Context, conn domain.ChannelConnection) (Session, error) {
	return p.action(ctx, conn, p.client.StartSession)
}

func (p *WahaProvider) StopSession(ctx context.Context, conn domain.ChannelConnection) (Session, error) {
	return p.action(ctx, conn, p.client.StopSession)
}

func (p *WahaProvider) RestartSession(ctx context.Context, conn domain.ChannelConnection) (Session, error) {
	return p.action(ctx, conn, p.client.RestartSession)
}

func (p *WahaProvider) GetSession(ctx context.Context, conn domain.ChannelConnection) (Session, error) {
	name, err := p.SessionRef(conn)
	if err != nil {
		return Session{}, err
	}
	return p.client.GetSession(ctx, name)
}

func (p *WahaProvider) GetQRCode(ctx context.Context, conn domain.ChannelConnection) (QRCode, error) {
	name, err := p.SessionRef(conn)
	if err != nil {
		return QRCode{}, err
	}
	return p.client.GetQRCode(ctx, name)
}

func (p *WahaProvider) GetMe(ctx context.Context, conn domain.ChannelConnection) (*Account, error) {
	name, err := p.SessionRef(conn)
	if err != nil {
		return nil, err
	}
	return p.client.GetMe(ctx, name)
}

func (p *WahaProvider) Metadata() ports.ProviderMetadata {
	return ports.ProviderMetadata{
		Name: domain.ProviderWAHA,
		Kind: domain.ProviderKindUnofficial,
		Capabilities: []domain.Capability{
			domain.CapabilityHealth,
			domain.CapabilitySessionPairing,
			domain.CapabilityQRPairing,
		},
	}
}

func (p *WahaProvider) IsConfigured(_ context.Context, conn domain.ChannelConnection) bool {
	return p != nil && p.client != nil && validateConnection(conn) == nil
}

func (p *WahaProvider) CheckHealth(ctx context.Context, conn domain.ChannelConnection) (ports.HealthStatus, error) {
	if err := validateConnection(conn); err != nil {
		return ports.HealthStatus{Reachable: false, Degraded: true}, err
	}
	if err := p.client.Health(ctx); err != nil {
		return ports.HealthStatus{Reachable: false, Degraded: true}, err
	}
	return ports.HealthStatus{Reachable: true}, nil
}

func (p *WahaProvider) SendText(context.Context, domain.ChannelConnection, domain.OutboundTextMessage) (*domain.SendResult, error) {
	return nil, ports.ErrCapabilityNotSupported
}

func (p *WahaProvider) SendMedia(context.Context, domain.ChannelConnection, domain.OutboundMediaMessage) (*domain.SendResult, error) {
	return nil, ports.ErrCapabilityNotSupported
}

func (p *WahaProvider) SendTemplate(context.Context, domain.ChannelConnection, domain.OutboundTemplateMessage) (*domain.SendResult, error) {
	return nil, ports.ErrCapabilityNotSupported
}

func (p *WahaProvider) DownloadMedia(context.Context, domain.ChannelConnection, domain.InboundMedia) (*domain.MediaContent, error) {
	return nil, ports.ErrCapabilityNotSupported
}

func (p *WahaProvider) HandleDeliveryStatus(context.Context, domain.ChannelConnection, []byte) (*domain.DeliveryStatusUpdate, error) {
	return nil, ports.ErrCapabilityNotSupported
}

// CanonicalStatus maps WAHA vocabulary to existing OMNIRA connection states.
// pending == connecting; active == connected. Legacy names stay stable for
// Meta compatibility while WAHA-specific states remain outside the domain.
func CanonicalStatus(status string) domain.ConnectionStatus {
	switch status {
	case "STOPPED":
		return domain.ConnectionStatusDisconnected
	case "STARTING", "SCAN_QR_CODE", "PASSKEY_REQUIRED", "PASSKEY_CONFIRMATION_REQUIRED":
		return domain.ConnectionStatusPending
	case "WORKING":
		return domain.ConnectionStatusActive
	case "FAILED":
		return domain.ConnectionStatusFailed
	default:
		return domain.ConnectionStatusDegraded
	}
}

func (p *WahaProvider) action(ctx context.Context, conn domain.ChannelConnection, action func(context.Context, string) (Session, error)) (Session, error) {
	name, err := p.SessionRef(conn)
	if err != nil {
		return Session{}, err
	}
	return action(ctx, name)
}

func validateConnection(conn domain.ChannelConnection) error {
	if conn.ID == uuid.Nil || conn.TenantID == uuid.Nil || conn.Provider != domain.ProviderWAHA || conn.ProviderKind != domain.ProviderKindUnofficial {
		return fmt.Errorf("%w: provider, kind, tenant and connection identity required", ErrInvalidConnection)
	}
	if conn.Status == domain.ConnectionStatusRevoked {
		return fmt.Errorf("%w: connection revoked", ErrInvalidConnection)
	}
	return nil
}
