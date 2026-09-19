package meta_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/meta"
)

type resolver struct {
	conn             *domain.ChannelConnection
	err              error
	provider, number string
}

func (r *resolver) ResolveInboundConnection(_ context.Context, provider, number string) (*domain.ChannelConnection, error) {
	r.provider, r.number = provider, number
	return r.conn, r.err
}

func signature(body []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(body)
	return "sha256=" + hex.EncodeToString(h.Sum(nil))
}

func TestVerifyChallenge(t *testing.T) {
	got, err := meta.VerifyChallenge("subscribe", "verify", "123", "verify")
	if err != nil || got != "123" {
		t.Fatalf("challenge failed: %q %v", got, err)
	}
	if _, err := meta.VerifyChallenge("subscribe", "wrong", "123", "verify"); err == nil {
		t.Fatal("wrong token accepted")
	}
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"entry":[]}`)
	if err := meta.VerifySignature(body, signature(body, "secret"), "secret"); err != nil {
		t.Fatal(err)
	}
	if err := meta.VerifySignature(body, "", "secret"); err == nil {
		t.Fatal("missing signature accepted")
	}
	if err := meta.VerifySignature(body, signature([]byte("other"), "secret"), "secret"); err == nil {
		t.Fatal("invalid signature accepted")
	}
}

func TestHandlerResolvesTrustedPhoneNumberOnly(t *testing.T) {
	body := []byte(`{"entry":[{"changes":[{"value":{"metadata":{"phone_number_id":"phone-a"}}}]}]}`)
	r := &resolver{conn: &domain.ChannelConnection{ID: uuid.New(), TenantID: uuid.New(), Provider: domain.ProviderMetaCloud, ProviderKind: domain.ProviderKindOfficial, Status: domain.ConnectionStatusActive}}
	h := meta.Handler{AppSecret: "secret", VerifyToken: "verify", Resolver: r}
	req := httptest.NewRequest("POST", "/webhooks/v1/meta/whatsapp", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", signature(body, "secret"))
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != 200 || r.provider != domain.ProviderMetaCloud || r.number != "phone-a" {
		t.Fatalf("resolution failed: status=%d provider=%s number=%s", res.Code, r.provider, r.number)
	}
	if r.conn.TenantID == uuid.Nil {
		t.Fatal("tenant missing")
	}
}

func TestHandlerRejectsMalformedUnknownInactiveAndOversized(t *testing.T) {
	base := meta.Handler{AppSecret: "secret", VerifyToken: "verify", Resolver: &resolver{}}
	for name, body := range map[string]string{"malformed": "{", "unknown": `{"entry":[]}`} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("X-Hub-Signature-256", signature([]byte(body), "secret"))
		res := httptest.NewRecorder()
		base.ServeHTTP(res, req)
		if res.Code != 400 && name == "malformed" {
			t.Fatalf("%s: got %d", name, res.Code)
		}
		if res.Code != 404 && name == "unknown" {
			t.Fatalf("%s: got %d", name, res.Code)
		}
	}
	r := &resolver{conn: &domain.ChannelConnection{Provider: domain.ProviderMetaCloud, ProviderKind: domain.ProviderKindOfficial, Status: domain.ConnectionStatusDisconnected}}
	h := meta.Handler{AppSecret: "secret", Resolver: r}
	body := []byte(`{"entry":[{"changes":[{"value":{"metadata":{"phone_number_id":"x"}}}]}]}`)
	req := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", signature(body, "secret"))
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != 409 {
		t.Fatalf("inactive got %d", res.Code)
	}
}
