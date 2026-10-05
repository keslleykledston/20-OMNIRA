package meta_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/meta"
	"github.com/omnira/omnira/internal/channels/ports"
)

func newClient(t *testing.T, h http.HandlerFunc) (*meta.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := meta.NewClient(srv.URL, "v21.0", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c, srv
}

func TestSendTextOK(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v21.0/12345678/messages" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("path=%s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.X"}]}`))
	})
	id, err := c.SendText(context.Background(), "tok", "12345678", "5592984517378", "oi")
	if err != nil || id != "wamid.X" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestSendTextErrorClassification(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{400, `{"error":{"code":131047}}`, ports.ErrSessionWindowClosed},
		{401, `{"error":{"code":190}}`, ports.ErrAuthentication},
		{429, `{"error":{"code":4}}`, ports.ErrRateLimited},
		{400, `{"error":{"code":100}}`, ports.ErrPermanent},
		{500, `{}`, ports.ErrOutcomeUnknown},
		{200, `{}`, ports.ErrOutcomeUnknown}, // accepted without id: it may have been sent
	}
	for _, tc := range cases {
		c, _ := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		})
		_, err := c.SendText(context.Background(), "tok", "12345678", "5592984517378", "oi")
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: got %v want %v", tc.status, err, tc.want)
		}
	}
}

func TestSendTextTimeoutAfterWriteIsOutcomeUnknown(t *testing.T) {
	c, srv := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		_ = conn.Close() // reset after the request was read
	})
	_ = srv
	_, err := c.SendText(context.Background(), "tok", "12345678", "5592984517378", "oi")
	if !errors.Is(err, ports.ErrOutcomeUnknown) {
		t.Fatalf("err=%v", err)
	}
}

func TestErrorsNeverCarryTheTokenOrBody(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"code":100,"message":"secret-body 5592984517378"}}`))
	})
	_, err := c.SendText(context.Background(), "SECRETTOKEN-1234567890", "12345678", "5592984517378", "oi")
	if err == nil || strings.Contains(err.Error(), "SECRETTOKEN") || strings.Contains(err.Error(), "secret-body") {
		t.Fatalf("err leaks: %v", err)
	}
}

func TestFetchMediaAllowlist(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"url":"https://evil.example.com/x","mime_type":"image/jpeg"}`))
	})
	if _, _, err := c.FetchMedia(context.Background(), "tok", "987654321"); !errors.Is(err, ports.ErrMediaSourceNotAllowed) {
		t.Fatalf("err=%v", err)
	}
}

func TestPhoneInfo(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"display_phone_number":"+55 92 98451-7378","verified_name":"K3G","status":"CONNECTED"}`))
	})
	info, err := c.PhoneInfo(context.Background(), "tok", "12345678")
	if err != nil || info.VerifiedName != "K3G" {
		t.Fatalf("%+v %v", info, err)
	}
}

func TestNewClientRejectsBadBase(t *testing.T) {
	for _, b := range []string{"http://graph.facebook.com", "https://user:pw@graph.facebook.com", "ftp://x"} {
		if _, err := meta.NewClient(b, "", nil); err == nil {
			t.Errorf("%s accepted", b)
		}
	}
}

func TestHandlerPerConnectionSecretAndChallenge(t *testing.T) {
	conn := &domain.ChannelConnection{ID: uuid.New(), TenantID: uuid.New(), Provider: domain.ProviderMetaCloud, ProviderKind: domain.ProviderKindOfficial, Status: domain.ConnectionStatusActive}
	h := meta.Handler{Secrets: fakeSecrets{secret: "other"}, Resolver: &resolver{conn: conn}, Intake: &fakeIntake{seen: map[string]bool{}}}
	if code := post(h, inboundBody); code != 401 { // signed with "secret", connection's secret is "other"
		t.Fatalf("wrong per-connection secret must be 401, got %d", code)
	}
	get := func(tok string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?hub.mode=subscribe&hub.verify_token="+tok+"&hub.challenge=abc", nil))
		return rec.Code
	}
	if get("omn-good") != 200 || get("nope") != 403 {
		t.Fatal("challenge must pass only for a valid per-connection token")
	}
}
