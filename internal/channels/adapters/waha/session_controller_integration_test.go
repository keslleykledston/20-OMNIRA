package waha_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/adapters/waha"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
)

type staticCreds struct{}

func (staticCreds) Store(context.Context, uuid.UUID, ports.Credential) (string, error) {
	return "", nil
}
func (staticCreds) Resolve(context.Context, string) (ports.Credential, error) {
	return ports.Credential{Fields: map[string]string{"webhook_hmac_key": "0123456789abcdef0123456789abcdef"}}, nil
}
func (staticCreds) Rotate(context.Context, string, ports.Credential) error { return nil }

// Opt-in: OMNIRA_WAHA_TEST_URL / OMNIRA_WAHA_TEST_KEY point at a throwaway WAHA.
func TestSessionControllerAgainstRealWAHA(t *testing.T) {
	url, key := os.Getenv("OMNIRA_WAHA_TEST_URL"), os.Getenv("OMNIRA_WAHA_TEST_KEY")
	if url == "" || key == "" {
		t.Skip("OMNIRA_WAHA_TEST_URL and OMNIRA_WAHA_TEST_KEY required")
	}
	client, err := waha.NewClient(url, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := waha.NewProvider(client, staticCreds{})
	if err != nil {
		t.Fatal(err)
	}
	ctrl := waha.NewSessionController(provider)
	id := uuid.New()
	conn := domain.ChannelConnection{ID: id, TenantID: uuid.New(), Channel: domain.ChannelWhatsApp, Provider: domain.ProviderWAHA,
		ProviderKind: domain.ProviderKindUnofficial, ProviderSessionRef: "omnira_" + id.String(), SecretRef: uuid.NewString()}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	t.Cleanup(func() { // best-effort removal of the throwaway session
		req, _ := http.NewRequest(http.MethodDelete, url+"/api/sessions/"+conn.ProviderSessionRef, nil)
		req.Header.Set("X-Api-Key", key)
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
		}
	})

	if s, err := ctrl.Status(ctx, conn); err != nil || s != ports.SessionMissing {
		t.Fatalf("unknown session: %q %v, want missing", s, err)
	}
	if err := ctrl.Create(ctx, conn, "https://omnira.example.com"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if s, err := ctrl.Status(ctx, conn); err != nil || s != ports.SessionStopped {
		t.Fatalf("after create: %q %v, want stopped", s, err)
	}
	if err := ctrl.Start(ctx, conn); err != nil {
		t.Fatalf("start: %v", err)
	}
	var last ports.SessionStatus
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		if last, err = ctrl.Status(ctx, conn); err != nil {
			t.Fatal(err)
		}
		if last == ports.SessionNeedsQR {
			break
		}
	}
	t.Logf("session status after start: %s", last)
	if last != ports.SessionNeedsQR {
		t.Fatalf("session never reached needs_qr (last=%s)", last)
	}
	qr, err := ctrl.QR(ctx, conn)
	if err != nil || qr.Data == "" || qr.MIMEType == "" {
		t.Fatalf("qr: %+v %v", qr, err)
	}
	if err := ctrl.Stop(ctx, conn); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if s, _ := ctrl.Status(ctx, conn); s != ports.SessionStopped {
		t.Fatalf("after stop: %s", s)
	}
}
