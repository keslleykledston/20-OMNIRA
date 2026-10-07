package meta

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
)

// Credential fields of a Meta Cloud connection (stored encrypted, ADR-0009).
const (
	FieldAccessToken = "access_token"
	FieldAppSecret   = "app_secret"
	FieldVerifyToken = "verify_token"
)

// Provider implements ports.ChannelProvider for the WhatsApp Cloud API (official). Everything is per connection:
// the access token and the app secret come from that connection's encrypted credential, resolved server-side at the
// moment of use. Authorization stays outside this adapter: callers resolve and authorize the connection first.
type Provider struct {
	client      *Client
	credentials ports.CredentialStore
	operations  metric.Int64Counter
}

var _ ports.ChannelProvider = (*Provider)(nil)

func NewProvider(client *Client, credentials ports.CredentialStore) (*Provider, error) {
	if client == nil || credentials == nil {
		return nil, ports.ErrConfiguration
	}
	operations, _ := otel.Meter("omnira/channels").Int64Counter("channel_operation_total")
	return &Provider{client: client, credentials: credentials, operations: operations}, nil
}

func (p *Provider) Metadata() ports.ProviderMetadata {
	return ports.ProviderMetadata{Name: domain.ProviderMetaCloud, Kind: domain.ProviderKindOfficial, Capabilities: capabilities()}
}

func capabilities() []domain.Capability {
	// Free text inside the 24 h window, approved templates any time, inbound media, delivery status. Outbound media is not offered yet.
	return []domain.Capability{domain.CapabilityText, domain.CapabilityTemplate, domain.CapabilityMedia, domain.CapabilityDeliveryStatus, domain.CapabilityHealth}
}

var ErrInvalidConnection = errors.New("meta: invalid channel connection")

func validateConnection(conn domain.ChannelConnection) error {
	if conn.ID == uuid.Nil || conn.TenantID == uuid.Nil || conn.Provider != domain.ProviderMetaCloud || conn.ProviderKind != domain.ProviderKindOfficial || !digitsPattern.MatchString(conn.ExternalNumberID) {
		return fmt.Errorf("%w: provider, kind, tenant, connection and phone number id required", ErrInvalidConnection)
	}
	if conn.Status == domain.ConnectionStatusRevoked {
		return fmt.Errorf("%w: connection revoked", ErrInvalidConnection)
	}
	return nil
}

// credential resolves this connection's secrets. A missing or incomplete credential is ErrNotConfigured, never a
// silent success.
func (p *Provider) credential(ctx context.Context, conn domain.ChannelConnection) (ports.Credential, error) {
	if conn.SecretRef == "" {
		return ports.Credential{}, ports.ErrNotConfigured
	}
	c, err := p.credentials.Resolve(ctx, conn.SecretRef)
	if err != nil {
		return ports.Credential{}, fmt.Errorf("%w: resolve credential", ports.ErrNotConfigured)
	}
	return c, nil
}

func (p *Provider) IsConfigured(ctx context.Context, conn domain.ChannelConnection) bool {
	if validateConnection(conn) != nil {
		return false
	}
	c, err := p.credential(ctx, conn)
	return err == nil && c.Fields[FieldAccessToken] != ""
}

// CheckHealth reads the number's status with the connection's own token (read-only).
func (p *Provider) CheckHealth(ctx context.Context, conn domain.ChannelConnection) (ports.HealthStatus, error) {
	if err := validateConnection(conn); err != nil {
		return ports.HealthStatus{Degraded: true}, err
	}
	c, err := p.credential(ctx, conn)
	if err != nil {
		return ports.HealthStatus{Degraded: true}, err
	}
	info, err := p.client.PhoneInfo(ctx, c.Fields[FieldAccessToken], conn.ExternalNumberID)
	if err != nil {
		if errors.Is(err, ports.ErrAuthentication) {
			return ports.HealthStatus{Reachable: true, Degraded: true, Detail: "token rejected"}, err
		}
		return ports.HealthStatus{Reachable: false, Degraded: true}, err
	}
	return ports.HealthStatus{Reachable: true, Degraded: strings.EqualFold(info.Status, "BANNED") || strings.EqualFold(info.Status, "RESTRICTED"), Detail: info.Status}, nil
}

// Probe is the read-only credential check used by "Testar conexão": it needs no stored connection yet.
func (p *Provider) Probe(ctx context.Context, fields map[string]string) (PhoneInfo, error) {
	return p.client.PhoneInfo(ctx, fields[FieldAccessToken], strings.TrimSpace(fields["phone_number_id"]))
}

func (p *Provider) VerifyWebhook(ctx context.Context, conn domain.ChannelConnection, req ports.WebhookVerificationRequest) error {
	if err := validateConnection(conn); err != nil {
		return err
	}
	c, err := p.credential(ctx, conn)
	if err != nil {
		return err
	}
	for k, v := range req.Headers {
		if strings.EqualFold(k, "X-Hub-Signature-256") {
			if VerifySignature(req.Body, v, c.Fields[FieldAppSecret]) != nil {
				return ports.ErrInvalidWebhookSignature
			}
			return nil
		}
	}
	return ports.ErrInvalidWebhookSignature
}

func (p *Provider) ParseInbound(_ context.Context, conn domain.ChannelConnection, payload []byte) (*domain.InboundMessage, error) {
	events, err := ParseEvents(conn, payload)
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		if ev.Message != nil {
			return ev.Message, nil
		}
	}
	return nil, ports.ErrCapabilityNotSupported
}

func (p *Provider) HandleDeliveryStatus(_ context.Context, conn domain.ChannelConnection, payload []byte) (*domain.DeliveryStatusUpdate, error) {
	events, err := ParseEvents(conn, payload)
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		if ev.Status != nil {
			return ev.Status, nil
		}
	}
	return nil, ports.ErrCapabilityNotSupported
}

func (p *Provider) count(ctx context.Context, op string, err error) {
	if p.operations == nil {
		return
	}
	p.operations.Add(ctx, 1, metric.WithAttributes(
		attribute.String("provider", domain.ProviderMetaCloud), attribute.String("provider_type", string(domain.ProviderKindOfficial)),
		attribute.String("operation", op), attribute.String("status", statusFor(err))))
}

func statusFor(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ports.ErrAuthentication):
		return "authentication"
	case errors.Is(err, ports.ErrSessionWindowClosed):
		return "window_closed"
	case errors.Is(err, ports.ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, ports.ErrOutcomeUnknown):
		return "outcome_unknown"
	case errors.Is(err, ports.ErrPermanent):
		return "permanent"
	case errors.Is(err, ports.ErrTransient):
		return "transient"
	default:
		return "unknown"
	}
}

// SendText sends free text inside the 24 h window. Meta has no idempotency key, so IdempotencyKey is not forwarded:
// the guarantee against a double send is that ONLY an answer proving "not executed" is retryable; an ambiguous one is
// ports.ErrOutcomeUnknown and ends the message as "uncertain" (see the delivery worker).
func (p *Provider) SendText(ctx context.Context, conn domain.ChannelConnection, msg domain.OutboundTextMessage) (*domain.SendResult, error) {
	if err := validateConnection(conn); err != nil {
		return nil, err
	}
	to := strings.TrimPrefix(strings.TrimSpace(msg.ToE164), "+")
	if strings.TrimSpace(msg.Text) == "" || !digitsPattern.MatchString(to) {
		return nil, fmt.Errorf("%w: recipient and text are required", ports.ErrPermanent)
	}
	c, err := p.credential(ctx, conn)
	if err != nil {
		return nil, err
	}
	id, err := p.client.SendText(ctx, c.Fields[FieldAccessToken], conn.ExternalNumberID, to, msg.Text)
	p.count(ctx, "send_text", err)
	if err != nil {
		if errors.Is(err, ports.ErrOutcomeUnknown) {
			log.Printf("meta: send outcome unknown connection_id=%s", conn.ID)
		}
		return nil, err
	}
	return &domain.SendResult{ProviderMessageID: id, State: domain.DeliveryStateSent}, nil
}

// SendMedia uploads the operator's file to Meta and sends it inside the 24 h window (ADR-0024). The upload is repeatable; the message is
// not (no idempotency key), so only the message step can end as ports.ErrOutcomeUnknown.
func (p *Provider) SendMedia(ctx context.Context, conn domain.ChannelConnection, msg domain.OutboundMediaMessage) (*domain.SendResult, error) {
	if err := validateConnection(conn); err != nil {
		return nil, err
	}
	to := strings.TrimPrefix(strings.TrimSpace(msg.ToE164), "+")
	if len(msg.Data) == 0 || msg.Mime == "" || !digitsPattern.MatchString(to) {
		return nil, fmt.Errorf("%w: recipient and media are required", ports.ErrPermanent)
	}
	c, err := p.credential(ctx, conn)
	if err != nil {
		return nil, err
	}
	token := c.Fields[FieldAccessToken]
	mediaID, err := p.client.UploadMedia(ctx, token, conn.ExternalNumberID, msg.Mime, msg.FileName, msg.Data)
	p.count(ctx, "upload_media", err)
	if err != nil {
		return nil, err
	}
	id, err := p.client.SendMedia(ctx, token, conn.ExternalNumberID, to, string(msg.Kind), mediaID, msg.Caption, msg.FileName)
	p.count(ctx, "send_media", err)
	if err != nil {
		if errors.Is(err, ports.ErrOutcomeUnknown) {
			log.Printf("meta: media send outcome unknown connection_id=%s", conn.ID)
		}
		return nil, err
	}
	return &domain.SendResult{ProviderMessageID: id, State: domain.DeliveryStateSent}, nil
}

// SendTemplate sends an approved template (allowed any time, which is how a conversation starts outside the 24 h
// window). Same delivery guarantees as SendText: an ambiguous failure is ports.ErrOutcomeUnknown, never retried.
func (p *Provider) SendTemplate(ctx context.Context, conn domain.ChannelConnection, msg domain.OutboundTemplateMessage) (*domain.SendResult, error) {
	if err := validateConnection(conn); err != nil {
		return nil, err
	}
	to := strings.TrimPrefix(strings.TrimSpace(msg.ToE164), "+")
	if msg.TemplateName == "" || msg.LanguageCode == "" || !digitsPattern.MatchString(to) {
		return nil, fmt.Errorf("%w: recipient, template name and language are required", ports.ErrPermanent)
	}
	c, err := p.credential(ctx, conn)
	if err != nil {
		return nil, err
	}
	id, err := p.client.SendTemplate(ctx, c.Fields[FieldAccessToken], conn.ExternalNumberID, to, msg.TemplateName, msg.LanguageCode, msg.Params)
	p.count(ctx, "send_template", err)
	if err != nil {
		if errors.Is(err, ports.ErrOutcomeUnknown) {
			log.Printf("meta: template send outcome unknown connection_id=%s", conn.ID)
		}
		return nil, err
	}
	return &domain.SendResult{ProviderMessageID: id, State: domain.DeliveryStateSent}, nil
}

// SendInteractive implements ports.InteractiveSender: reply buttons or a list, inside the 24 h window.
func (p *Provider) SendInteractive(ctx context.Context, conn domain.ChannelConnection, msg domain.OutboundInteractiveMessage) (*domain.SendResult, error) {
	if err := validateConnection(conn); err != nil {
		return nil, err
	}
	to := strings.TrimPrefix(strings.TrimSpace(msg.ToE164), "+")
	if !digitsPattern.MatchString(to) {
		return nil, fmt.Errorf("%w: recipient is required", ports.ErrPermanent)
	}
	c, err := p.credential(ctx, conn)
	if err != nil {
		return nil, err
	}
	id, err := p.client.SendInteractive(ctx, c.Fields[FieldAccessToken], conn.ExternalNumberID, to, msg)
	p.count(ctx, "send_interactive", err)
	if err != nil {
		if errors.Is(err, ports.ErrOutcomeUnknown) {
			log.Printf("meta: interactive send outcome unknown connection_id=%s", conn.ID)
		}
		return nil, err
	}
	return &domain.SendResult{ProviderMessageID: id, State: domain.DeliveryStateSent}, nil
}

var _ ports.InteractiveSender = (*Provider)(nil)

// ListTemplates reads the WhatsApp Business Account's templates with this connection's own token.
func (p *Provider) ListTemplates(ctx context.Context, conn domain.ChannelConnection) ([]Template, error) {
	if err := validateConnection(conn); err != nil {
		return nil, err
	}
	c, err := p.credential(ctx, conn)
	if err != nil {
		return nil, err
	}
	out, err := p.client.ListTemplates(ctx, c.Fields[FieldAccessToken], conn.ExternalAccountID)
	p.count(ctx, "list_templates", err)
	return out, err
}

// NewMessageID: Meta assigns the id in the send answer; there is nothing to reserve ahead of time.
func (p *Provider) NewMessageID(context.Context, domain.ChannelConnection) (string, error) {
	return "", ports.ErrCapabilityNotSupported
}

func (p *Provider) DownloadMedia(ctx context.Context, conn domain.ChannelConnection, media domain.InboundMedia) (*domain.MediaContent, error) {
	if err := validateConnection(conn); err != nil {
		return nil, err
	}
	if media.MediaRef == "" || media.Kind == "" {
		return nil, fmt.Errorf("%w: media reference and kind required", ports.ErrPermanent)
	}
	c, err := p.credential(ctx, conn)
	if err != nil {
		return nil, err
	}
	data, mime, err := p.client.FetchMedia(ctx, c.Fields[FieldAccessToken], media.MediaRef)
	p.count(ctx, "download_media", err)
	if err != nil {
		return nil, err
	}
	if media.MimeType != "" {
		mime = media.MimeType
	}
	return &domain.MediaContent{Kind: media.Kind, MimeType: mime, Data: data, SizeBytes: int64(len(data))}, nil
}
