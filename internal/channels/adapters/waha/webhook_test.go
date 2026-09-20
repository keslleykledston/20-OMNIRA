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
	status    *domain.DeliveryStatusUpdate
	duplicate bool
	err       error
}

func (i *webhookIntake) ProcessWebhook(_ context.Context, _ domain.ChannelConnection, _, _ string, _ string, message *domain.InboundMessage, status *domain.DeliveryStatusUpdate) (bool, error) {
	i.calls++
	i.message = message
	i.status = status
	duplicate := i.duplicate
	i.duplicate = true
	return duplicate, i.err
}

func TestWebhookHandlerNormalizesAckForIntake(t *testing.T) {
	conn := webhookConnection()
	conn.SecretRef = "credential-a"
	intake := &webhookIntake{}
	handler := waha.NewWebhookHandler(newProvider(t, "hmac-secret"), wahaResolver{conn: &conn}).UseIntake(intake)
	body := []byte(`{"id":"evt-ack","event":"message.ack","session":"` + sessionName(conn) + `","payload":{"id":"msg-out","ackName":"DEVICE","timestamp":1710000000}}`)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, signedRequest("/webhooks/v1/whatsapp/waha/"+conn.ID.String(), body, "hmac-secret"))
	if res.Code != http.StatusOK || intake.status == nil || intake.status.State != domain.DeliveryStateDelivered {
		t.Fatalf("ack not normalized: code=%d status=%+v", res.Code, intake.status)
	}
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

// O WhatsApp entrega remetentes como "@lid" (Linked ID), um identificador
// opaco que não é telefone. O número real vem em _data.Info.SenderAlt e é ele
// que deve virar o E.164 do contato — caso contrário a mensagem é recusada
// (observado em produção 2026-09-20: 400 "malformed webhook") ou, pior, o
// contato nasceria com uma identidade falsa derivada do LID.
func TestParseInboundResolvesLinkedIDSenderFromSenderAlt(t *testing.T) {
	conn := webhookConnection()
	provider := newProvider(t, "secret")
	body := []byte(`{"id":"evt-lid","event":"message.any","session":"` + sessionName(conn) + `","payload":{"id":"false_175222334484588@lid_2A6E","timestamp":1710000000,"from":"175222334484588@lid","fromMe":false,"body":"oi","hasMedia":false,"_data":{"Info":{"SenderAlt":"559291740090@s.whatsapp.net"}}}}`)
	parsed, err := provider.ParseWebhook(conn, body)
	if err != nil {
		t.Fatalf("lid sender rejected: %v", err)
	}
	if parsed.Message == nil {
		t.Fatal("no message parsed")
	}
	if parsed.Message.FromE164 != "+559291740090" {
		t.Fatalf("sender not resolved from SenderAlt: %q", parsed.Message.FromE164)
	}
	// O LID nunca pode virar telefone: seria um contato com identidade falsa.
	if strings.Contains(parsed.Message.FromE164, "175222334484588") {
		t.Fatalf("opaque LID leaked into E.164: %q", parsed.Message.FromE164)
	}

	// Sem SenderAlt não há número confiável; recusar continua sendo o correto.
	noAlt := `{"id":"evt-lid2","event":"message.any","session":"` + sessionName(conn) + `","payload":{"id":"m2","timestamp":1710000000,"from":"175222334484588@lid","fromMe":false,"body":"oi"}}`
	if _, err := provider.ParseWebhook(conn, []byte(noAlt)); err == nil {
		t.Fatal("lid without SenderAlt accepted as E.164")
	}
}

// O nome de perfil (pushName) é escolhido por quem envia, então serve como
// rótulo de exibição e nunca como identidade — e precisa ser higienizado antes
// de chegar à UI e aos logs.
func TestParseInboundCarriesSanitizedSenderName(t *testing.T) {
	conn := webhookConnection()
	provider := newProvider(t, "secret")
	msg := func(push string) []byte {
		return []byte(`{"id":"evt-n","event":"message.any","session":"` + sessionName(conn) + `","payload":{"id":"m1","timestamp":1710000000,"from":"5511999999999@c.us","fromMe":false,"body":"oi","_data":{"Info":{"PushName":` + push + `}}}}`)
	}
	parsed, err := provider.ParseWebhook(conn, msg(`"K3G Solutions"`))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Message.SenderName != "K3G Solutions" {
		t.Fatalf("sender name not carried: %q", parsed.Message.SenderName)
	}
	// A identidade continua sendo o telefone, não o nome.
	if parsed.Message.FromE164 != "+5511999999999" {
		t.Fatalf("identity changed by sender name: %q", parsed.Message.FromE164)
	}

	dirty, err := provider.ParseWebhook(conn, msg(`"  Evil\u0000Corp\nSupport  "`))
	if err != nil {
		t.Fatal(err)
	}
	if dirty.Message.SenderName != "EvilCorp Support" {
		t.Fatalf("control characters survived: %q", dirty.Message.SenderName)
	}

	long, err := provider.ParseWebhook(conn, msg(`"`+strings.Repeat("a", 200)+`"`))
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(long.Message.SenderName)); n != 80 {
		t.Fatalf("sender name not truncated: %d runes", n)
	}

	none, err := provider.ParseWebhook(conn, []byte(`{"id":"evt-n2","event":"message.any","session":"`+sessionName(conn)+`","payload":{"id":"m2","timestamp":1710000000,"from":"5511999999999@c.us","fromMe":false,"body":"oi"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if none.Message.SenderName != "" {
		t.Fatalf("expected empty sender name, got %q", none.Message.SenderName)
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

// A failure to read the connection's credential is ours, not the sender's: 503 (WAHA retries), not 401.
// The session runner wraps verification (needed for RLS) and receives the connection's tenant.
func TestWebhookHandlerDistinguishesUnreadableKeyFromBadSignatureAndScopesSession(t *testing.T) {
	conn := webhookConnection()
	conn.SecretRef = "credential-a"
	body := []byte(`{"id":"evt-1","event":"message.any","session":"` + sessionName(conn) + `","payload":{"id":"msg-1","timestamp":1710000000,"from":"5511999999999@c.us","body":"oi"}}`)
	path := "/webhooks/v1/whatsapp/waha/" + conn.ID.String()
	post := func(h *waha.WebhookHandler) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
		req.Header.Set("X-Webhook-Hmac", hmacSignature(body, "hmac-secret"))
		req.Header.Set("X-Webhook-Hmac-Algorithm", "sha512")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res.Code
	}

	// Provider without a credential store: not configured -> 503.
	client, err := waha.NewClient("http://waha:3000", "api-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	bare, err := waha.NewProvider(client)
	if err != nil {
		t.Fatal(err)
	}
	if code := post(waha.NewWebhookHandler(bare, wahaResolver{conn: &conn})); code != http.StatusServiceUnavailable {
		t.Fatalf("unreadable key got %d, want 503", code)
	}

	// The session runner receives the resolved connection's tenant and wraps verification.
	var gotTenant uuid.UUID
	ran := false
	handler := waha.NewWebhookHandler(newProvider(t, "hmac-secret"), wahaResolver{conn: &conn}).
		UseSession(func(ctx context.Context, tenantID uuid.UUID, fn func(context.Context) error) error {
			gotTenant, ran = tenantID, true
			return fn(ctx)
		})
	if code := post(handler); code != http.StatusOK || !ran || gotTenant != conn.TenantID {
		t.Fatalf("session runner: code=%d ran=%v tenant=%s want %s", code, ran, gotTenant, conn.TenantID)
	}
}
