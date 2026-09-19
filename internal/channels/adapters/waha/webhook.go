package waha

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const MaxWebhookBody = 1 << 20

var (
	ErrInvalidWebhookSignature = fmt.Errorf("%w: waha signature mismatch", ports.ErrInvalidWebhookSignature)
	ErrMalformedWebhook        = errors.New("waha: malformed webhook")
	ErrUnsupportedWebhookEvent = errors.New("waha: unsupported webhook event")
	ErrIgnoredWebhookMessage   = errors.New("waha: ignored provider-originated message")
)

type WebhookRequest struct {
	Headers map[string]string
	Body    []byte
}

// VerifyWebhook authenticates raw WAHA bytes before JSON parsing. HMAC key
// lives in CredentialStore under webhook_hmac_key and never leaves adapter.
func (p *WahaProvider) VerifyWebhook(ctx context.Context, conn domain.ChannelConnection, req ports.WebhookVerificationRequest) error {
	if err := validateConnection(conn); err != nil {
		return err
	}
	if p.credentials == nil || conn.SecretRef == "" {
		return ports.ErrNotConfigured
	}
	credential, err := p.credentials.Resolve(ctx, conn.SecretRef)
	if err != nil {
		return errors.New("waha: resolve webhook credential failed")
	}
	secret := credential.Fields["webhook_hmac_key"]
	if secret == "" {
		return ports.ErrNotConfigured
	}
	if !verifyHMAC(req.Body, req.Headers["X-Webhook-Hmac"], req.Headers["X-Webhook-Hmac-Algorithm"], secret) {
		return ErrInvalidWebhookSignature
	}
	return nil
}

func verifyHMAC(body []byte, signature, algorithm, secret string) bool {
	if !strings.EqualFold(algorithm, "sha512") || signature == "" {
		return false
	}
	want, err := hex.DecodeString(signature)
	if err != nil || len(want) != sha512.Size {
		return false
	}
	h := hmac.New(sha512.New, []byte(secret))
	_, _ = h.Write(body)
	got := h.Sum(nil)
	return subtle.ConstantTimeCompare(want, got) == 1
}

type webhookEnvelope struct {
	ID      string          `json:"id"`
	Event   string          `json:"event"`
	Session string          `json:"session"`
	Payload json.RawMessage `json:"payload"`
}

type webhookMessage struct {
	ID        string  `json:"id"`
	Timestamp float64 `json:"timestamp"`
	From      string  `json:"from"`
	FromMe    bool    `json:"fromMe"`
	Body      string  `json:"body"`
	HasMedia  bool    `json:"hasMedia"`
	Media     *struct {
		URL      string `json:"url"`
		MIMEType string `json:"mimetype"`
		Filename string `json:"filename"`
	} `json:"media"`
}

type ParsedWebhook struct {
	EventID          string
	DeduplicationKey string
	Event            string
	Message          *domain.InboundMessage
}

// ParseWebhook normalizes message/message.any and safely acknowledges
// message.ack/session.status without exposing WAHA payload structs.
func (p *WahaProvider) ParseWebhook(conn domain.ChannelConnection, body []byte) (ParsedWebhook, error) {
	var envelope webhookEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Event == "" || envelope.Session == "" {
		return ParsedWebhook{}, ErrMalformedWebhook
	}
	expectedSession, err := p.SessionRef(conn)
	if err != nil {
		return ParsedWebhook{}, err
	}
	if envelope.Session != expectedSession {
		return ParsedWebhook{}, ErrSessionOwnership
	}
	if envelope.ID == "" {
		return ParsedWebhook{}, ErrMalformedWebhook
	}
	result := ParsedWebhook{EventID: envelope.ID, DeduplicationKey: envelope.ID, Event: envelope.Event}
	if envelope.Event != "message" && envelope.Event != "message.any" {
		if envelope.Event != "message.ack" && envelope.Event != "session.status" {
			return ParsedWebhook{}, ErrUnsupportedWebhookEvent
		}
		return result, nil
	}
	var payload webhookMessage
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil || payload.ID == "" || payload.From == "" {
		return ParsedWebhook{}, ErrMalformedWebhook
	}
	result.DeduplicationKey = payload.ID
	if payload.FromMe {
		return result, ErrIgnoredWebhookMessage
	}
	from, err := normalizeSender(payload.From)
	if err != nil {
		return ParsedWebhook{}, err
	}
	if payload.Body == "" && !payload.HasMedia {
		return ParsedWebhook{}, ErrMalformedWebhook
	}
	message := &domain.InboundMessage{
		ProviderMessageID: payload.ID,
		ConnectionID:      conn.ID.String(),
		FromE164:          from,
		Text:              payload.Body,
		Timestamp:         time.Unix(int64(payload.Timestamp), int64((payload.Timestamp-float64(int64(payload.Timestamp)))*1e9)).UTC(),
	}
	if payload.Timestamp <= 0 {
		return ParsedWebhook{}, ErrMalformedWebhook
	}
	if payload.HasMedia && payload.Media != nil {
		message.Media = &domain.InboundMedia{
			Kind:     mediaKind(payload.Media.MIMEType),
			MediaRef: firstNonEmpty(payload.Media.URL, payload.Media.Filename),
			MimeType: payload.Media.MIMEType,
		}
	}
	result.Message = message
	return result, nil
}

func (p *WahaProvider) ParseInbound(ctx context.Context, conn domain.ChannelConnection, payload []byte) (*domain.InboundMessage, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	parsed, err := p.ParseWebhook(conn, payload)
	if err != nil {
		return nil, err
	}
	if parsed.Message == nil {
		return nil, ErrUnsupportedWebhookEvent
	}
	return parsed.Message, nil
}

type WahaConnectionResolver interface {
	ResolveWahaConnection(ctx context.Context, connectionToken string) (*domain.ChannelConnection, error)
}

type WebhookHandler struct {
	Provider *WahaProvider
	Resolver WahaConnectionResolver
	Events   ports.WebhookEventStore
	MaxBody  int64
	webhook  metric.Int64Counter
	invalid  metric.Int64Counter
}

func NewWebhookHandler(provider *WahaProvider, resolver WahaConnectionResolver, stores ...ports.WebhookEventStore) *WebhookHandler {
	meter := otel.Meter("omnira/channels")
	webhook, _ := meter.Int64Counter("channel_webhook_total")
	invalid, _ := meter.Int64Counter("channel_webhook_invalid_total")
	var events ports.WebhookEventStore
	if len(stores) > 0 {
		events = stores[0]
	}
	return &WebhookHandler{Provider: provider, Resolver: resolver, Events: events, MaxBody: MaxWebhookBody, webhook: webhook, invalid: invalid}
}

func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.addMetric(r.Context(), h.webhook, "received")
	if r.Method != http.MethodPost {
		h.reject(r.Context(), w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.Provider == nil || h.Resolver == nil {
		h.reject(r.Context(), w, http.StatusServiceUnavailable, "webhook unavailable")
		return
	}
	token := webhookToken(r.URL.Path)
	if token == "" {
		h.reject(r.Context(), w, http.StatusNotFound, "unknown connection")
		return
	}
	conn, err := h.Resolver.ResolveWahaConnection(r.Context(), token)
	if err != nil || conn == nil {
		h.reject(r.Context(), w, http.StatusNotFound, "unknown connection")
		return
	}
	if conn.Provider != domain.ProviderWAHA || conn.ProviderKind != domain.ProviderKindUnofficial {
		h.reject(r.Context(), w, http.StatusConflict, "provider mismatch")
		return
	}
	if conn.Status != domain.ConnectionStatusActive {
		h.reject(r.Context(), w, http.StatusConflict, "connection inactive")
		return
	}
	limit := h.MaxBody
	if limit <= 0 {
		limit = MaxWebhookBody
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		h.reject(r.Context(), w, http.StatusRequestEntityTooLarge, "payload too large or unreadable")
		return
	}
	if err := h.Provider.VerifyWebhook(r.Context(), *conn, ports.WebhookVerificationRequest{
		Headers: map[string]string{
			"X-Webhook-Hmac":           r.Header.Get("X-Webhook-Hmac"),
			"X-Webhook-Hmac-Algorithm": r.Header.Get("X-Webhook-Hmac-Algorithm"),
		},
		Body: body,
	}); err != nil {
		h.reject(r.Context(), w, http.StatusUnauthorized, "invalid webhook signature")
		return
	}
	parsed, parseErr := h.Provider.ParseWebhook(*conn, body)
	if parseErr != nil && !errors.Is(parseErr, ErrIgnoredWebhookMessage) {
		if errors.Is(parseErr, ErrUnsupportedWebhookEvent) {
			h.reject(r.Context(), w, http.StatusAccepted, "event ignored")
			return
		}
		h.reject(r.Context(), w, http.StatusBadRequest, "malformed webhook")
		return
	}
	if h.Events != nil {
		digest := sha256.Sum256(body)
		duplicate, err := h.Events.MarkReceived(r.Context(), *conn, parsed.DeduplicationKey, parsed.Event, hex.EncodeToString(digest[:]))
		if err != nil {
			h.reject(r.Context(), w, http.StatusServiceUnavailable, "webhook intake unavailable")
			return
		}
		if duplicate {
			h.addMetric(r.Context(), h.webhook, "duplicate")
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	h.addMetric(r.Context(), h.webhook, "accepted")
	w.WriteHeader(http.StatusOK)
}

func (h *WebhookHandler) reject(ctx context.Context, w http.ResponseWriter, status int, message string) {
	h.addMetric(ctx, h.invalid, strconv.Itoa(status))
	http.Error(w, message, status)
}

func (h *WebhookHandler) addMetric(ctx context.Context, counter metric.Int64Counter, status string) {
	if counter == nil {
		return
	}
	counter.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("provider", domain.ProviderWAHA),
			attribute.String("provider_type", string(domain.ProviderKindUnofficial)),
			attribute.String("operation", "webhook"),
			attribute.String("status", status),
		))
}

func webhookToken(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 5 || parts[0] != "webhooks" || parts[1] != "v1" || parts[2] != "whatsapp" || parts[3] != "waha" {
		return ""
	}
	return parts[4]
}

func normalizeSender(jid string) (string, error) {
	parts := strings.SplitN(jid, "@", 2)
	if len(parts) != 2 || (parts[1] != "c.us" && parts[1] != "s.whatsapp.net") {
		return "", fmt.Errorf("waha: unsupported sender address")
	}
	value := strings.TrimPrefix(parts[0], "+")
	if len(value) < 7 || len(value) > 15 {
		return "", fmt.Errorf("waha: invalid sender address")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("waha: invalid sender address")
		}
	}
	return "+" + value, nil
}

func mediaKind(mime string) domain.MediaKind {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return domain.MediaKindImage
	case strings.HasPrefix(mime, "video/"):
		return domain.MediaKindVideo
	case strings.HasPrefix(mime, "audio/"):
		return domain.MediaKindAudio
	default:
		return domain.MediaKindDocument
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
