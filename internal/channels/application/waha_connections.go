package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

const PermissionChannelManage = "channel.manage"

var (
	ErrConnForbidden       = errors.New("channel: forbidden")
	ErrConnNotFound        = errors.New("channel: connection not found")
	ErrRiskNotAcknowledged = errors.New("channel: unofficial provider risk must be acknowledged")
	ErrPublicURLMissing    = errors.New("channel: public webhook base URL is not configured")
	ErrQRUnavailable       = errors.New("channel: no QR code available in the current session state")
)

// ConnectionView is the only shape ever returned to clients: no secret
// material, no secret_ref, no webhook key.
type ConnectionView struct {
	ID                 uuid.UUID
	Provider           string
	ProviderKind       domain.ProviderKind
	Status             domain.ConnectionStatus
	SessionStatus      ports.SessionStatus
	ExternalAccountID  string
	Capabilities       []domain.Capability
	RiskAcknowledgedAt *time.Time
	CreatedAt          time.Time
}

// WahaConnectionService manages unofficial WhatsApp (WAHA) connections for the
// TenantContext tenant. Every operation requires channel.manage; tenant and
// actor are taken only from the TenantContext.
type WahaConnectionService struct {
	conns       ports.ChannelConnectionRepository
	credentials ports.CredentialStore
	sessions    ports.SessionController
	perms       ports.PermissionChecker
	audit       ports.ChannelAudit
	publicURL   string
}

func NewWahaConnectionService(conns ports.ChannelConnectionRepository, credentials ports.CredentialStore, sessions ports.SessionController, perms ports.PermissionChecker, audit ports.ChannelAudit, publicWebhookBaseURL string) *WahaConnectionService {
	return &WahaConnectionService{conns: conns, credentials: credentials, sessions: sessions, perms: perms, audit: audit, publicURL: strings.TrimRight(publicWebhookBaseURL, "/")}
}

func (s *WahaConnectionService) authorize(ctx context.Context) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil || tc.Source != tenancydomain.AccessSourceDirect {
		return nil, ErrConnForbidden
	}
	ok, err := s.perms.HasPermission(ctx, tc.ActorID, PermissionChannelManage)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrConnForbidden
	}
	return tc, nil
}

// load returns the tenant's WAHA connection or ErrConnNotFound. RLS hides
// other tenants' rows; the explicit tenant check is defense in depth.
func (s *WahaConnectionService) load(ctx context.Context, tc *tenancydomain.TenantContext, id uuid.UUID) (*domain.ChannelConnection, error) {
	conn, err := s.conns.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if conn == nil || conn.TenantID != tc.TenantID || conn.Provider != domain.ProviderWAHA {
		return nil, ErrConnNotFound
	}
	return conn, nil
}

func view(c *domain.ChannelConnection, session ports.SessionStatus) ConnectionView {
	return ConnectionView{ID: c.ID, Provider: c.Provider, ProviderKind: c.ProviderKind, Status: c.Status, SessionStatus: session,
		ExternalAccountID: c.ExternalAccountID, Capabilities: c.Capabilities, RiskAcknowledgedAt: c.RiskAcknowledgedAt, CreatedAt: c.CreatedAt}
}

// Create registers a pending WAHA connection. The unofficial-provider risk
// acknowledgement is mandatory and recorded with the acknowledging actor.
func (s *WahaConnectionService) Create(ctx context.Context, riskAcknowledged bool) (ConnectionView, error) {
	tc, err := s.authorize(ctx)
	if err != nil {
		return ConnectionView{}, err
	}
	if !riskAcknowledged {
		return ConnectionView{}, ErrRiskNotAcknowledged
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return ConnectionView{}, fmt.Errorf("channel: generate webhook key: %w", err)
	}
	now := time.Now().UTC()
	id := uuid.New()
	conn := &domain.ChannelConnection{
		ID: id, TenantID: tc.TenantID, Channel: domain.ChannelWhatsApp, Provider: domain.ProviderWAHA,
		ProviderKind: domain.ProviderKindUnofficial, ExternalNumberID: id.String(), ProviderSessionRef: "omnira_" + id.String(),
		Status:             domain.ConnectionStatusPending,
		Capabilities:       []domain.Capability{domain.CapabilityText, domain.CapabilityDeliveryStatus, domain.CapabilitySessionPairing, domain.CapabilityQRPairing},
		RiskAcknowledgedAt: &now, RiskAcknowledgedBy: &tc.ActorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.conns.Store(ctx, conn); err != nil {
		return ConnectionView{}, err
	}
	ref, err := s.credentials.Store(ctx, conn.ID, ports.Credential{Fields: map[string]string{"webhook_hmac_key": hex.EncodeToString(key)}})
	if err != nil {
		return ConnectionView{}, err
	}
	conn.SecretRef = ref
	if err := s.conns.Update(ctx, conn); err != nil {
		return ConnectionView{}, err
	}
	if err := s.audit.Record(ctx, "channel.connection_created", "channel_connection", conn.ID, map[string]any{"provider": conn.Provider, "risk_acknowledged": true}); err != nil {
		return ConnectionView{}, err
	}
	return view(conn, ports.SessionMissing), nil
}

func (s *WahaConnectionService) List(ctx context.Context) ([]ConnectionView, error) {
	tc, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	all, err := s.conns.FindByTenant(ctx, tc.TenantID)
	if err != nil {
		return nil, err
	}
	out := []ConnectionView{}
	for _, c := range all {
		if c.Provider == domain.ProviderWAHA && c.TenantID == tc.TenantID {
			out = append(out, view(c, ""))
		}
	}
	return out, nil
}

// Get refreshes the live session state and persists the resulting status.
func (s *WahaConnectionService) Get(ctx context.Context, id uuid.UUID) (ConnectionView, error) {
	tc, err := s.authorize(ctx)
	if err != nil {
		return ConnectionView{}, err
	}
	conn, err := s.load(ctx, tc, id)
	if err != nil {
		return ConnectionView{}, err
	}
	return s.refresh(ctx, conn)
}

func (s *WahaConnectionService) refresh(ctx context.Context, conn *domain.ChannelConnection) (ConnectionView, error) {
	status, err := s.sessions.Status(ctx, *conn)
	if err != nil {
		return ConnectionView{}, err
	}
	next := conn.Status
	switch status {
	case ports.SessionWorking:
		if conn.RequiresRiskAcknowledgement() {
			next = domain.ConnectionStatusPending
		} else {
			next = domain.ConnectionStatusActive
		}
	case ports.SessionStarting, ports.SessionNeedsQR, ports.SessionMissing:
		next = domain.ConnectionStatusPending
	case ports.SessionStopped:
		next = domain.ConnectionStatusDisconnected
	case ports.SessionFailed:
		next = domain.ConnectionStatusFailed
	}
	changed := next != conn.Status
	if status == ports.SessionWorking && conn.ExternalAccountID == "" {
		if acct, err := s.sessions.Account(ctx, *conn); err == nil && acct != "" {
			conn.ExternalAccountID, changed = acct, true
		}
	}
	if changed {
		conn.Status = next
		if err := s.conns.Update(ctx, conn); err != nil {
			return ConnectionView{}, err
		}
	}
	return view(conn, status), nil
}

// StartSession is idempotent: it creates the provider session (with this
// connection's webhook) only when missing, and starts it unless already up.
func (s *WahaConnectionService) StartSession(ctx context.Context, id uuid.UUID) (ConnectionView, error) {
	tc, err := s.authorize(ctx)
	if err != nil {
		return ConnectionView{}, err
	}
	conn, err := s.load(ctx, tc, id)
	if err != nil {
		return ConnectionView{}, err
	}
	if conn.RequiresRiskAcknowledgement() {
		return ConnectionView{}, ErrRiskNotAcknowledged
	}
	status, err := s.sessions.Status(ctx, *conn)
	if err != nil {
		return ConnectionView{}, err
	}
	if status == ports.SessionMissing {
		if s.publicURL == "" {
			return ConnectionView{}, ErrPublicURLMissing
		}
		if err := s.sessions.Create(ctx, *conn, s.publicURL); err != nil {
			return ConnectionView{}, err
		}
	}
	if status != ports.SessionWorking && status != ports.SessionStarting && status != ports.SessionNeedsQR {
		if err := s.sessions.Start(ctx, *conn); err != nil {
			return ConnectionView{}, err
		}
	}
	if err := s.audit.Record(ctx, "channel.session_started", "channel_connection", conn.ID, map[string]any{"previous_session_status": string(status)}); err != nil {
		return ConnectionView{}, err
	}
	return s.refresh(ctx, conn)
}

func (s *WahaConnectionService) StopSession(ctx context.Context, id uuid.UUID) (ConnectionView, error) {
	tc, err := s.authorize(ctx)
	if err != nil {
		return ConnectionView{}, err
	}
	conn, err := s.load(ctx, tc, id)
	if err != nil {
		return ConnectionView{}, err
	}
	status, err := s.sessions.Status(ctx, *conn)
	if err != nil {
		return ConnectionView{}, err
	}
	if status != ports.SessionMissing && status != ports.SessionStopped {
		if err := s.sessions.Stop(ctx, *conn); err != nil {
			return ConnectionView{}, err
		}
	}
	if err := s.audit.Record(ctx, "channel.session_stopped", "channel_connection", conn.ID, map[string]any{"previous_session_status": string(status)}); err != nil {
		return ConnectionView{}, err
	}
	return s.refresh(ctx, conn)
}

// QR returns the pairing code only while the session is waiting for a scan.
func (s *WahaConnectionService) QR(ctx context.Context, id uuid.UUID) (ports.QRImage, error) {
	tc, err := s.authorize(ctx)
	if err != nil {
		return ports.QRImage{}, err
	}
	conn, err := s.load(ctx, tc, id)
	if err != nil {
		return ports.QRImage{}, err
	}
	status, err := s.sessions.Status(ctx, *conn)
	if err != nil {
		return ports.QRImage{}, err
	}
	if status != ports.SessionNeedsQR {
		return ports.QRImage{}, ErrQRUnavailable
	}
	return s.sessions.QR(ctx, *conn)
}
