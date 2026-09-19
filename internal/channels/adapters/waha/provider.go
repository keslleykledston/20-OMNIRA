package waha

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
)

var (
	ErrInvalidConnection = errors.New("waha: invalid channel connection")
	ErrSessionOwnership  = errors.New("waha: provider session reference does not belong to connection")
)

// WahaProvider owns WAHA-specific session lifecycle. Authorization remains
// outside this adapter: callers must resolve and authorize conn first.
type WahaProvider struct {
	client *Client
}

func NewProvider(client *Client) (*WahaProvider, error) {
	if client == nil {
		return nil, ErrConfiguration
	}
	return &WahaProvider{client: client}, nil
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

func (p *WahaProvider) CheckHealth(ctx context.Context) error {
	return p.client.Health(ctx)
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
