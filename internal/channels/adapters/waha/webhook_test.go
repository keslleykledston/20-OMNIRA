package waha_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/adapters/waha"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
)

type credentialStore struct {
	credential ports.Credential
	err        error
}

func (s credentialStore) Store(context.Context, uuid.UUID, ports.Credential) (string, error) {
	return "", nil
}
func (s credentialStore) Resolve(context.Context, string) (ports.Credential, error) {
	return s.credential, s.err
}
func (s credentialStore) Rotate(context.Context, string, ports.Credential) error { return nil }

type wahaResolver struct {
	conn *domain.ChannelConnection
	err  error
}

type webhookEventStore struct {
	duplicate bool
	keys      []string
}

type webhookIntake struct {
	calls     int
	message   *domain.InboundMessage
	duplicate bool
	err       error
}

func (i *webhookIntake) ProcessWebhook(_ context.Context, _ domain.ChannelConnection, _, _ string, _ string, message *domain.InboundMessage) (bool, error) {
	i.calls++
	i.message = message
	duplicate := i.duplicate
	i.duplicate = true
	return duplicate, i.err
}

func (s *webhookEventStore) MarkReceived(_ context.Context, _ domain.ChannelConnection, eventID, eventType, digest string) (bool, error) {
	s.keys = append(s.keys, eventID+":"+eventType+":"+digest)
	duplicate := s.duplicate
	s.duplicate = true
	return duplicate, nil
}

func (r wahaResolver) ResolveWahaConnection(context.Context, string) (*domain.ChannelConnection, error) {
	return r.conn, r.err
}

func TestVerifyWebhookUsesSHA512RawBody(t *testing.T) {
	conn := webhookConnection()
	conn.SecretRef = "credential-a"
	body := []byte(`{"event":"message.any","session":"omnira_ignored"}`)
	provider := newProvider(t, "hmac-secret")
	sig := hmacSignature(body, "hmac-secret")
	req := ports.WebhookVerificationRequest{Body: body, Headers: map[string]string{
		"X-Webhook-Hmac": sig, "X-Webhook-Hmac-Algorithm": "sha512",
	}}
	if err := provider.VerifyWebhook(context.Background(), conn, req); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []ports.WebhookVerificationRequest{
		{Body: body, Headers: map[string]string{"X-Webhook-Hmac": "", "X-Webhook-Hmac-Algorithm": "sha512"}},
		{Body: body, Headers: map[string]string{"X-Webhook-Hmac": sig, "X-Webhook-Hmac-Algorithm": "sha256"}},
		{Body: []byte("changed"), Headers: map[string]string{"X-Webhook-Hmac": sig, "X-Webhook-Hmac-Algorithm": "sha512"}},
	} {
		if err := provider.VerifyWebhook(context.Background(), conn, tc); !errors.Is(err, waha.ErrInvalidWebhookSignature) {
			t.Fatalf("invalid webhook accepted: %v", err)
		}
	}
}

func TestParseInboundCanonicalizesMessageAndRejectsProviderEcho(t *testing.T) {
	conn := webhookConnection()
	provider := newProvider(t, "secret")
	body := []byte(`{"id":"evt-1","event":"message.any","session":"` + sessionName(conn) + `","payload":{"id":"msg-1","timestamp":1710000000.25,"from":"5511999999999@c.us","fromMe":false,"body":"oi","hasMedia":true,"media":{"url":"http://waha/api/file","mimetype":"image/jpeg"}}}`)
	parsed, err := provider.ParseWebhook(conn, body)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.EventID != "evt-1" || parsed.Message == nil {
		t.Fatalf("unexpected parsed event: %+v", parsed)
	}
	if parsed.Message.ProviderMessageID != "msg-1" || parsed.Message.FromE164 != "+5511999999999" || parsed.Message.Text != "oi" {
		t.Fatalf("unexpected canonical message: %+v", parsed.Message)
	}
	if parsed.Message.Media == nil || parsed.Message.Media.Kind != domain.MediaKindImage {
		t.Fatalf("media not normalized: %+v", parsed.Message.Media)
	}

	echo := strings.Replace(string(body), `"fromMe":false`, `"fromMe":true`, 1)
	_, err = provider.ParseWebhook(conn, []byte(echo))
	if !errors.Is(err, waha.ErrIgnoredWebhookMessage) {
		t.Fatalf("provider echo not ignored: %v", err)
	}
}

func TestParseInboundRejectsSessionConfusionAndMalformedSender(t *testing.T) {
	conn := webhookConnection()
	provider := newProvider(t, "secret")
	wrongSession := `{"id":"evt-1","event":"message","session":"other","payload":{"id":"msg-1","timestamp":1710000000,"from":"5511999999999@c.us","body":"oi"}}`
	if _, err := provider.ParseWebhook(conn, []byte(wrongSession)); !errors.Is(err, waha.ErrSessionOwnership) {
		t.Fatalf("wrong session accepted: %v", err)
	}
	group := `{"id":"evt-1","event":"message","session":"` + sessionName(conn) + `","payload":{"id":"msg-1","timestamp":1710000000,"from":"123@g.us","body":"oi"}}`
	if _, err := provider.ParseWebhook(conn, []byte(group)); err == nil {
		t.Fatal("group sender accepted as E.164")
	}
}

func TestWebhookHandlerAuthenticatesResolvesAndLimits(t *testing.T) {
	conn := webhookConnection()
	conn.SecretRef = "credential-a"
	provider := newProvider(t, "hmac-secret")
	handler := waha.NewWebhookHandler(provider, wahaResolver{conn: &conn})
	body := []byte(`{"id":"evt-1","event":"message.any","session":"` + sessionName(conn) + `","payload":{"id":"msg-1","timestamp":1710000000,"from":"5511999999999@c.us","body":"oi"}}`)
	path := "/webhooks/v1/whatsapp/waha/" + conn.ID.String()

	valid := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	valid.Header.Set("X-Webhook-Hmac", hmacSignature(body, "hmac-secret"))
	valid.Header.Set("X-Webhook-Hmac-Algorithm", "sha512")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, valid)
	if res.Code != http.StatusOK {
		t.Fatalf("valid webhook got %d: %s", res.Code, res.Body.String())
	}

	for name, signature := range map[string]string{"missing": "", "wrong": hmacSignature(body, "wrong-secret")} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
		req.Header.Set("X-Webhook-Hmac", signature)
		req.Header.Set("X-Webhook-Hmac-Algorithm", "sha512")
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, req)
		if got.Code != http.StatusUnauthorized {
			t.Fatalf("%s signature got %d", name, got.Code)
		}
	}

	limited := waha.NewWebhookHandler(provider, wahaResolver{conn: &conn})
	limited.MaxBody = 8
	large := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	large.Header.Set("X-Webhook-Hmac", hmacSignature(body, "hmac-secret"))
	large.Header.Set("X-Webhook-Hmac-Algorithm", "sha512")
	tooLarge := httptest.NewRecorder()
	limited.ServeHTTP(tooLarge, large)
	if tooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body limit got %d", tooLarge.Code)
	}
}

func TestWebhookHandlerRejectsUnknownInactiveAndProviderConfusion(t *testing.T) {
	conn := webhookConnection()
	provider := newProvider(t, "hmac-secret")
	body := []byte(`{"id":"evt-1","event":"session.status","session":"` + sessionName(conn) + `","payload":{"status":"WORKING"}}`)
	path := "/webhooks/v1/whatsapp/waha/" + conn.ID.String()

	unknown := waha.NewWebhookHandler(provider, wahaResolver{err: errors.New("not found")})
	req := signedRequest(path, body, "hmac-secret")
	res := httptest.NewRecorder()
	unknown.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("unknown got %d", res.Code)
	}

	inactive := conn
	inactive.Status = domain.ConnectionStatusDisconnected
	h := waha.NewWebhookHandler(provider, wahaResolver{conn: &inactive})
	res = httptest.NewRecorder()
	h.ServeHTTP(res, signedRequest(path, body, "hmac-secret"))
	if res.Code != http.StatusConflict {
		t.Fatalf("inactive got %d", res.Code)
	}

	official := conn
	official.Provider = domain.ProviderMetaCloud
	official.ProviderKind = domain.ProviderKindOfficial
	h = waha.NewWebhookHandler(provider, wahaResolver{conn: &official})
	res = httptest.NewRecorder()
	h.ServeHTTP(res, signedRequest(path, body, "hmac-secret"))
	if res.Code != http.StatusConflict {
		t.Fatalf("provider confusion got %d", res.Code)
	}
}

func TestWebhookHandlerAcknowledgesRedeliveryAfterDurableReservation(t *testing.T) {
	conn := webhookConnection()
	conn.SecretRef = "credential-a"
	provider := newProvider(t, "hmac-secret")
	events := &webhookEventStore{}
	handler := waha.NewWebhookHandler(provider, wahaResolver{conn: &conn}, events)
	body := []byte(`{"id":"evt-1","event":"message.any","session":"` + sessionName(conn) + `","payload":{"id":"msg-1","timestamp":1710000000,"from":"5511999999999@c.us","body":"oi"}}`)
	path := "/webhooks/v1/whatsapp/waha/" + conn.ID.String()
	for i := 0; i < 2; i++ {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, signedRequest(path, body, "hmac-secret"))
		if res.Code != http.StatusOK {
			t.Fatalf("delivery %d got %d", i+1, res.Code)
		}
	}
	if len(events.keys) != 2 || !strings.HasPrefix(events.keys[0], "msg-1:message.any:") {
		t.Fatalf("dedupe key not prepared: %#v", events.keys)
	}
}

func TestWebhookHandlerSendsCanonicalMessageToAtomicIntake(t *testing.T) {
	conn := webhookConnection()
	conn.SecretRef = "credential-a"
	provider := newProvider(t, "hmac-secret")
	intake := &webhookIntake{}
	handler := waha.NewWebhookHandler(provider, wahaResolver{conn: &conn}).UseIntake(intake)
	body := []byte(`{"id":"evt-1","event":"message.any","session":"` + sessionName(conn) + `","payload":{"id":"msg-1","timestamp":1710000000,"from":"5511999999999@c.us","body":"oi"}}`)
	path := "/webhooks/v1/whatsapp/waha/" + conn.ID.String()
	for range 2 {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, signedRequest(path, body, "hmac-secret"))
		if res.Code != http.StatusOK {
			t.Fatalf("webhook got %d: %s", res.Code, res.Body.String())
		}
	}
	if intake.calls != 2 || intake.message == nil || intake.message.ProviderMessageID != "msg-1" {
		t.Fatalf("canonical intake not called: %+v", intake)
	}
}

func newProvider(t *testing.T, secret string) *waha.WahaProvider {
	t.Helper()
	client, err := waha.NewClient("http://waha:3000", "api-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := waha.NewProvider(client, credentialStore{credential: ports.Credential{Fields: map[string]string{"webhook_hmac_key": secret}}})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func webhookConnection() domain.ChannelConnection {
	return domain.ChannelConnection{ID: uuid.New(), TenantID: uuid.New(), Channel: domain.ChannelWhatsApp, Provider: domain.ProviderWAHA, ProviderKind: domain.ProviderKindUnofficial, Status: domain.ConnectionStatusActive}
}

func sessionName(conn domain.ChannelConnection) string { return "omnira_" + conn.ID.String() }

func hmacSignature(body []byte, secret string) string {
	h := hmac.New(sha512.New, []byte(secret))
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func signedRequest(path string, body []byte, secret string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set("X-Webhook-Hmac", hmacSignature(body, secret))
	req.Header.Set("X-Webhook-Hmac-Algorithm", "sha512")
	return req
}
