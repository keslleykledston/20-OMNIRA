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

type Handler struct {
	VerifyToken string
	AppSecret   string
	Resolver    ConnectionResolver
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
	if h.Resolver == nil || h.AppSecret == "" {
		http.Error(w, "webhook unavailable", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxWebhookBody))
	if err != nil {
		http.Error(w, "payload too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	if err := VerifySignature(body, r.Header.Get("X-Hub-Signature-256"), h.AppSecret); err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
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
	if err := json.Unmarshal(body, &envelope); err != nil {
		http.Error(w, "malformed payload", http.StatusBadRequest)
		return
	}
	phoneID := ""
	if len(envelope.Entry) > 0 && len(envelope.Entry[0].Changes) > 0 {
		phoneID = envelope.Entry[0].Changes[0].Value.Metadata.PhoneNumberID
	}
	if phoneID == "" {
		http.Error(w, "unknown connection", http.StatusNotFound)
		return
	}
	conn, err := h.Resolver.ResolveInboundConnection(r.Context(), domain.ProviderMetaCloud, phoneID)
	if err != nil || conn == nil {
		http.Error(w, "unknown connection", http.StatusNotFound)
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
	w.WriteHeader(http.StatusOK)
}

func (h Handler) challenge(w http.ResponseWriter, r *http.Request) {
	challenge, err := VerifyChallenge(r.URL.Query().Get("hub.mode"), r.URL.Query().Get("hub.verify_token"), r.URL.Query().Get("hub.challenge"), h.VerifyToken)
	if err != nil {
		http.Error(w, "invalid verification", http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, challenge)
}
