package waha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

// SendText requires msg.IdempotencyKey to already carry the stable,
// pre-reserved WAHA message id (see NewMessageID) — this adapter forwards it
// verbatim as the request's "id" field and never mints its own. Callers must
// persist that id durably BEFORE the first SendText attempt and reuse the
// exact same value on every retry/redelivery (PILOT.4A1); this adapter has
// no way to enforce that itself.
func (p *WahaProvider) SendText(ctx context.Context, conn domain.ChannelConnection, msg domain.OutboundTextMessage) (*domain.SendResult, error) {
	if err := validateConnection(conn); err != nil {
		return nil, err
	}
	if !e164Pattern.MatchString(msg.ToE164) || strings.TrimSpace(msg.Text) == "" || strings.TrimSpace(msg.IdempotencyKey) == "" {
		return nil, fmt.Errorf("%w: recipient, text and a reserved message id are required", ports.ErrPermanent)
	}
	name, err := p.SessionRef(conn)
	if err != nil {
		return nil, err
	}
	chatID, err := outboundChatID(msg.ProviderChatID, msg.ToE164)
	if err != nil {
		return nil, err
	}
	providerID, err := p.client.SendText(ctx, name, chatID, msg.Text, msg.IdempotencyKey)
	p.operations.Add(ctx, 1, metric.WithAttributes(attribute.String("provider", domain.ProviderWAHA), attribute.String("provider_type", string(domain.ProviderKindUnofficial)), attribute.String("operation", "send_text"), attribute.String("status", statusForError(err))))
	if err != nil {
		p.errors.Add(ctx, 1, metric.WithAttributes(attribute.String("provider", domain.ProviderWAHA), attribute.String("provider_type", string(domain.ProviderKindUnofficial)), attribute.String("operation", "send_text"), attribute.String("status", statusForError(err))))
		return nil, err
	}
	if !providerIDMatchesReserved(providerID, msg.IdempotencyKey) {
		// Protocol anomaly, not silently accepted: the reserved id is what
		// every durability guarantee in PILOT.4A1 depends on being echoed
		// back unchanged. Distinct sentinel from ErrUnknown (PILOT.4A2):
		// the delivery layer must NOT retry this automatically — see
		// ports.ErrProviderIDMismatch doc. This adapter has no OMNIRA
		// message_id to correlate with (only connection_id) — the
		// message-correlated terminal log is the worker's job (PILOT.4B
		// logUncertain); neither id value is printed here, per policy.
		log.Printf("waha: sendText response id mismatch connection_id=%s provider_id_mismatch=true", conn.ID)
		return nil, ports.ErrProviderIDMismatch
	}
	return &domain.SendResult{ProviderMessageID: providerID, State: domain.DeliveryStateSent}, nil
}

// providerIDMatchesReserved reports whether the id WAHA answered with is the id we reserved.
// Some engines echo it verbatim; GOWS answers with the serialized form
// "<fromMe>_<chatId>_<id>[_<participant>]" (the same form its message.ack webhooks use), so the
// reserved id must be exactly the id segment — a substring elsewhere does not count. Whatever
// WAHA returned is what gets stored, because that is the form later acks correlate against.
func providerIDMatchesReserved(got, reserved string) bool {
	if got == reserved {
		return true
	}
	parts := strings.Split(got, "_")
	return len(parts) >= 3 && (parts[0] == "true" || parts[0] == "false") && parts[2] == reserved
}

// NewMessageID reserves a stable message id from WAHA with no delivery side
// effect. Callers must persist the returned id durably before calling
// SendText.
func (p *WahaProvider) NewMessageID(ctx context.Context, conn domain.ChannelConnection) (string, error) {
	if err := validateConnection(conn); err != nil {
		return "", err
	}
	name, err := p.SessionRef(conn)
	if err != nil {
		return "", err
	}
	return p.client.NewMessageID(ctx, name)
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
	case errors.Is(err, ports.ErrProviderIDMismatch):
		return "provider_id_mismatch"
	default:
		return "unknown"
	}
}

// SendMedia sends an operator's file (ADR-0024). WAHA has no idempotency id for media, so a failure that may have reached WhatsApp (timeout,
// 5xx, reset, undecodable answer) is ports.ErrOutcomeUnknown and ends the message as "uncertain": a second attempt could deliver the file
// twice to the customer. Only an explicit throttle (429) or a rejection (4xx) keeps its meaning.
func (p *WahaProvider) SendMedia(ctx context.Context, conn domain.ChannelConnection, msg domain.OutboundMediaMessage) (*domain.SendResult, error) {
	if err := validateConnection(conn); err != nil {
		return nil, err
	}
	if len(msg.Data) == 0 || strings.TrimSpace(msg.Mime) == "" {
		return nil, fmt.Errorf("%w: media bytes and type are required", ports.ErrPermanent)
	}
	name, err := p.SessionRef(conn)
	if err != nil {
		return nil, err
	}
	chatID, err := outboundChatID(msg.ProviderChatID, msg.ToE164)
	if err != nil {
		return nil, err
	}
	endpoint, mimetype := mediaEndpoint(msg.Kind, msg.Mime)
	id, err := p.client.SendMedia(ctx, name, chatID, endpoint, mimetype, msg.FileName, msg.Data, msg.Caption)
	p.operations.Add(ctx, 1, metric.WithAttributes(attribute.String("provider", domain.ProviderWAHA), attribute.String("provider_type", string(domain.ProviderKindUnofficial)), attribute.String("operation", "send_media"), attribute.String("status", statusForError(err))))
	if err != nil {
		p.errors.Add(ctx, 1, metric.WithAttributes(attribute.String("provider", domain.ProviderWAHA), attribute.String("provider_type", string(domain.ProviderKindUnofficial)), attribute.String("operation", "send_media"), attribute.String("status", statusForError(err))))
		return nil, ambiguousMediaFailure(err, conn.ID.String())
	}
	return &domain.SendResult{ProviderMessageID: id, State: domain.DeliveryStateSent}, nil
}

// mediaEndpoint picks WAHA's endpoint: voice notes (OGG/Opus) go as voice, everything else by kind.
func mediaEndpoint(kind domain.MediaKind, mime string) (endpoint, mimetype string) {
	switch kind {
	case domain.MediaKindImage:
		return "sendImage", mime
	case domain.MediaKindVideo:
		return "sendVideo", mime
	case domain.MediaKindAudio:
		if mime == "audio/ogg" {
			return "sendVoice", "audio/ogg; codecs=opus"
		}
		return "sendFile", mime
	}
	return "sendFile", mime
}

// ambiguousMediaFailure maps every failure that may have reached the provider to ErrOutcomeUnknown (see SendMedia).
func ambiguousMediaFailure(err error, connectionID string) error {
	switch {
	case errors.Is(err, ports.ErrAuthentication), errors.Is(err, ports.ErrSessionDisconnected), errors.Is(err, ports.ErrConfiguration),
		errors.Is(err, ports.ErrRateLimited), errors.Is(err, ports.ErrPermanent):
		return err // proven not sent, or a throttle before processing
	}
	log.Printf("waha: media send outcome unknown connection_id=%s", connectionID)
	return fmt.Errorf("%w: %v", ports.ErrOutcomeUnknown, err)
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

// chatIDPattern aceita os endereços que o WhatsApp usa: telefone (c.us /
// s.whatsapp.net), Linked ID (lid) e grupo (g.us).
var chatIDPattern = regexp.MustCompile(`^[0-9]{5,20}(-[0-9]{1,20})?@(c\.us|s\.whatsapp\.net|lid|g\.us)$`)

// outboundChatID escolhe o endereço de envio.
//
// Prefere o endereço que o provedor informou na conversa. Derivar o destino do
// telefone ("<e164>@c.us") parece equivalente e não é: quando o contato é
// endereçado por LID, o WhatsApp ACEITA a mensagem no endereço derivado
// (ack=1 SERVER) e nunca a entrega — falha silenciosa, sem erro para o
// operador. Observado em produção 2026-09-20; o mesmo texto chegou a ack=2
// DEVICE quando endereçado ao LID.
//
// O valor persistido veio do provedor, então é validado antes de virar chamada
// de API: um endereço malformado é erro permanente, não algo a retransmitir.
func outboundChatID(providerChatID, toE164 string) (string, error) {
	if chatID := strings.TrimSpace(providerChatID); chatID != "" {
		if !chatIDPattern.MatchString(chatID) {
			return "", fmt.Errorf("%w: malformed provider chat id", ports.ErrPermanent)
		}
		return chatID, nil
	}
	return strings.TrimPrefix(toE164, "+") + "@c.us", nil
}

// ListGroups lists the groups of the connection's WhatsApp account (for the administrator to choose
// which ones to read, ADR-0015).
func (p *WahaProvider) ListGroups(ctx context.Context, conn domain.ChannelConnection) ([]domain.ProviderGroup, error) {
	if err := validateConnection(conn); err != nil {
		return nil, err
	}
	name, err := p.SessionRef(conn)
	if err != nil {
		return nil, err
	}
	return p.client.ListGroups(ctx, name)
}
