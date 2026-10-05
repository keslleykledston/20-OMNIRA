package meta

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/omnira/omnira/internal/channels/domain"
)

const MaxWebhookBody = 1 << 20

var (
	ErrInvalidVerifyToken = errors.New("meta webhook: invalid verify token")
	ErrInvalidSignature   = errors.New("meta webhook: invalid signature")
	ErrUnknownConnection  = errors.New("meta webhook: unknown connection")
	ErrInactiveConnection = errors.New("meta webhook: inactive connection")
)

func VerifyChallenge(mode, token, challenge, expectedToken string) (string, error) {
	if mode != "subscribe" || expectedToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(expectedToken)) != 1 {
		return "", ErrInvalidVerifyToken
	}
	if challenge == "" {
		return "", ErrInvalidVerifyToken
	}
	return challenge, nil
}

func VerifySignature(body []byte, header, appSecret string) error {
	if appSecret == "" || !strings.HasPrefix(header, "sha256=") {
		return ErrInvalidSignature
	}
	want, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil || len(want) != sha256.Size {
		return ErrInvalidSignature
	}
	h := hmac.New(sha256.New, []byte(appSecret))
	_, _ = h.Write(body)
	if !hmac.Equal(want, h.Sum(nil)) {
		return ErrInvalidSignature
	}
	return nil
}

type ConnectionResolver interface {
	ResolveInboundConnection(ctx context.Context, providerName, externalNumberID string) (*domain.ChannelConnection, error)
}

// Intake atomically dedupes a provider event and applies its inbound effects
// in the connection's tenant (implemented by inbox/adapters.WebhookIntake).
type Intake interface {
	ProcessWebhook(ctx context.Context, connection domain.ChannelConnection, deduplicationKey, eventType, payloadDigest string, message *domain.InboundMessage, status *domain.DeliveryStatusUpdate) (duplicate bool, err error)
}

// SecretResolver supplies the secrets of the webhook PER CONNECTION. There is no global app secret or verify token.
type SecretResolver interface {
	// AppSecret is the app secret of this connection (it signs every payload sent for it).
	AppSecret(ctx context.Context, conn *domain.ChannelConnection) (string, error)
	// CheckVerifyToken validates the handshake token against the connection it names.
	CheckVerifyToken(ctx context.Context, token string) (bool, error)
}

type Handler struct {
	Resolver ConnectionResolver
	Secrets  SecretResolver
	Intake   Intake // optional; nil keeps verify-only behavior
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.challenge(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.Resolver == nil || h.Secrets == nil {
		http.Error(w, "webhook unavailable", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxWebhookBody))
	if err != nil {
		http.Error(w, "payload too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	var envelope struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					Metadata struct {
						PhoneNumberID string `json:"phone_number_id"`
					} `json:"metadata"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	// The body is untrusted until its signature is checked with the secret of the connection it NAMES: it is only used
	// to find which secret to check with. An unknown number and a bad signature are the same answer (no oracle).
	if err := json.Unmarshal(body, &envelope); err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	phoneID := ""
	if len(envelope.Entry) > 0 && len(envelope.Entry[0].Changes) > 0 {
		phoneID = envelope.Entry[0].Changes[0].Value.Metadata.PhoneNumberID
	}
	if phoneID == "" {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	conn, err := h.Resolver.ResolveInboundConnection(r.Context(), domain.ProviderMetaCloud, phoneID)
	if err != nil || conn == nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	secret, err := h.Secrets.AppSecret(r.Context(), conn)
	if err != nil || VerifySignature(body, r.Header.Get("X-Hub-Signature-256"), secret) != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	if conn.Status != domain.ConnectionStatusActive {
		http.Error(w, "connection inactive", http.StatusConflict)
		return
	}
	if conn.ProviderKind != domain.ProviderKindOfficial {
		http.Error(w, "provider mismatch", http.StatusConflict)
		return
	}
	if h.Intake != nil {
		events, err := ParseEvents(*conn, body)
		if err != nil {
			http.Error(w, "malformed payload", http.StatusBadRequest)
			return
		}
		sum := sha256.Sum256(body)
		digest := hex.EncodeToString(sum[:])
		for _, ev := range events {
			if _, err := h.Intake.ProcessWebhook(r.Context(), *conn, ev.Key, ev.Type, digest, ev.Message, ev.Status); err != nil {
				// 5xx makes Meta retry; already-processed events dedupe on retry.
				http.Error(w, "webhook intake unavailable", http.StatusServiceUnavailable)
				return
			}
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (h Handler) challenge(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if h.Secrets == nil || q.Get("hub.mode") != "subscribe" || q.Get("hub.challenge") == "" {
		http.Error(w, "invalid verification", http.StatusForbidden)
		return
	}
	if ok, err := h.Secrets.CheckVerifyToken(r.Context(), q.Get("hub.verify_token")); err != nil || !ok {
		http.Error(w, "invalid verification", http.StatusForbidden)
		return
	}
	challenge := q.Get("hub.challenge")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, challenge)
}
