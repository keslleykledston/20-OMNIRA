package waha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var e164Pattern = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

var (
	ErrInvalidConnection = errors.New("waha: invalid channel connection")
	ErrSessionOwnership  = errors.New("waha: provider session reference does not belong to connection")
)

// WahaProvider owns WAHA-specific session lifecycle. Authorization remains
// outside this adapter: callers must resolve and authorize conn first.
type WahaProvider struct {
	client      *Client
	credentials ports.CredentialStore
	operations  metric.Int64Counter
	errors      metric.Int64Counter
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
	meter := otel.Meter("omnira/channels")
	operations, _ := meter.Int64Counter("channel_operation_total")
	errors, _ := meter.Int64Counter("channel_operation_error_total")
	return &WahaProvider{client: client, credentials: credentials, operations: operations, errors: errors}, nil
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
			domain.CapabilityText,
			domain.CapabilityMedia,
			domain.CapabilityDeliveryStatus,
			domain.CapabilityReadStatus,
			domain.CapabilityHealth,
			domain.CapabilitySessionPairing,
			domain.CapabilityQRPairing,
		},
	}
}

// Descriptor declares the WAHA onboarding flow without exposing runtime
// configuration or credentials. Disabled deployments still publish it so
// administrators understand why the integration cannot be selected.
func Descriptor(enabled bool, unavailableReason string) ports.ProviderDescriptor {
	return ports.ProviderDescriptor{
		ID:            domain.ProviderWAHA,
		Name:          "WhatsApp (não oficial)",
		Channel:       domain.ChannelWhatsApp,
		Kind:          domain.ProviderKindUnofficial,
		ConnectMethod: ports.ConnectMethodQRSession,
		RiskNotice:    "A automação não oficial pode causar o banimento do número. Use um número dedicado e aceite o risco antes de continuar.",
		Capabilities: []domain.Capability{
			domain.CapabilityText,
			domain.CapabilityDeliveryStatus,
			domain.CapabilitySessionPairing,
			domain.CapabilityQRPairing,
		},
		Enabled: enabled, UnavailableReason: unavailableReason,
		Inputs: []ports.ProviderInputDescriptor{}, Displays: []ports.ProviderDisplayDescriptor{},
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

func (p *WahaProvider) SendText(ctx context.Context, conn domain.ChannelConnection, msg domain.OutboundTextMessage) (*domain.SendResult, error) {
	if err := validateConnection(conn); err != nil {
		return nil, err
	}
	if !e164Pattern.MatchString(msg.ToE164) || strings.TrimSpace(msg.Text) == "" || strings.TrimSpace(msg.IdempotencyKey) == "" {
		return nil, fmt.Errorf("%w: recipient, text and idempotency key are required", ports.ErrPermanent)
	}
	name, err := p.SessionRef(conn)
	if err != nil {
		return nil, err
	}
	providerID, err := p.client.SendText(ctx, name, strings.TrimPrefix(msg.ToE164, "+")+"@c.us", msg.Text)
	p.operations.Add(ctx, 1, metric.WithAttributes(attribute.String("provider", domain.ProviderWAHA), attribute.String("provider_type", string(domain.ProviderKindUnofficial)), attribute.String("operation", "send_text"), attribute.String("status", statusForError(err))))
	if err != nil {
		p.errors.Add(ctx, 1, metric.WithAttributes(attribute.String("provider", domain.ProviderWAHA), attribute.String("provider_type", string(domain.ProviderKindUnofficial)), attribute.String("operation", "send_text"), attribute.String("status", statusForError(err))))
		return nil, err
	}
	return &domain.SendResult{ProviderMessageID: providerID, State: domain.DeliveryStateSent}, nil
}

func statusForError(err error) string {
	if err == nil {
		return "success"
	}
	switch {
	case errors.Is(err, ports.ErrAuthentication):
		return "authentication"
	case errors.Is(err, ports.ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, ports.ErrSessionDisconnected):
		return "session_disconnected"
	case errors.Is(err, ports.ErrProviderUnavailable):
		return "provider_unavailable"
	case errors.Is(err, ports.ErrConfiguration):
		return "configuration"
	case errors.Is(err, ports.ErrPermanent):
		return "permanent"
	case errors.Is(err, ports.ErrTransient):
		return "transient"
	default:
		return "unknown"
	}
}

func (p *WahaProvider) SendMedia(context.Context, domain.ChannelConnection, domain.OutboundMediaMessage) (*domain.SendResult, error) {
	return nil, ports.ErrCapabilityNotSupported
}

func (p *WahaProvider) SendTemplate(context.Context, domain.ChannelConnection, domain.OutboundTemplateMessage) (*domain.SendResult, error) {
	return nil, ports.ErrCapabilityNotSupported
}

func (p *WahaProvider) DownloadMedia(ctx context.Context, conn domain.ChannelConnection, media domain.InboundMedia) (*domain.MediaContent, error) {
	if err := validateConnection(conn); err != nil {
		return nil, err
	}
	if media.MediaRef == "" || media.Kind == "" {
		return nil, fmt.Errorf("%w: media reference and kind required", ports.ErrPermanent)
	}
	data, contentType, err := p.client.DownloadMedia(ctx, media.MediaRef)
	if err != nil {
		return nil, err
	}
	if media.MimeType != "" {
		contentType = media.MimeType
	}
	return &domain.MediaContent{Kind: media.Kind, MimeType: contentType, Data: data, SizeBytes: int64(len(data))}, nil
}

func (p *WahaProvider) HandleDeliveryStatus(ctx context.Context, conn domain.ChannelConnection, payload []byte) (*domain.DeliveryStatusUpdate, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return p.parseDeliveryStatus(conn, payload)
}

func (p *WahaProvider) parseDeliveryStatus(conn domain.ChannelConnection, payload []byte) (*domain.DeliveryStatusUpdate, error) {
	var envelope webhookEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Event != "message.ack" || envelope.Session == "" {
		return nil, ErrMalformedWebhook
	}
	expected, err := p.SessionRef(conn)
	if err != nil {
		return nil, err
	}
	if envelope.Session != expected {
		return nil, ErrSessionOwnership
	}
	var ack struct {
		ID        string  `json:"id"`
		AckName   string  `json:"ackName"`
		Timestamp float64 `json:"timestamp"`
	}
	if err := json.Unmarshal(envelope.Payload, &ack); err != nil || ack.ID == "" {
		return nil, ErrMalformedWebhook
	}
	state, ok := deliveryState(ack.AckName)
	if !ok {
		return nil, ErrUnsupportedWebhookEvent
	}
	occurred := time.Now().UTC()
	if ack.Timestamp > 0 {
		occurred = time.Unix(int64(ack.Timestamp), int64((ack.Timestamp-float64(int64(ack.Timestamp)))*1e9)).UTC()
	}
	return &domain.DeliveryStatusUpdate{ProviderMessageID: ack.ID, State: state, OccurredAt: occurred, Reason: ack.AckName}, nil
}

func deliveryState(ack string) (domain.DeliveryState, bool) {
	switch ack {
	case "ERROR":
		return domain.DeliveryStateFailed, true
	case "PENDING":
		return domain.DeliveryStateQueued, true
	case "SERVER":
		return domain.DeliveryStateSent, true
	case "DEVICE":
		return domain.DeliveryStateDelivered, true
	case "READ", "PLAYED":
		return domain.DeliveryStateRead, true
	default:
		return "", false
	}
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
