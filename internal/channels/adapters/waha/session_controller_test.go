package waha_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnira/omnira/internal/channels/adapters/waha"
	"github.com/omnira/omnira/internal/channels/ports"
)

// PILOT.4C §12: the read-only session status contract, exercised against a
// fake WAHA HTTP server — no real provider mutation, no QR/session lifecycle.
func newStatusServer(t *testing.T, handler http.HandlerFunc) *waha.SessionController {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := waha.NewClient(srv.URL, "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	provider, err := waha.NewProvider(client)
	if err != nil {
		t.Fatal(err)
	}
	return waha.NewSessionController(provider)
}

func TestSessionControllerStatusWorking(t *testing.T) {
	ctrl := newStatusServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"session","status":"WORKING"}`))
	})
	status, err := ctrl.Status(context.Background(), wahaConnection())
	if err != nil || status != ports.SessionWorking {
		t.Fatalf("status=%q err=%v, want working", status, err)
	}
}

func TestSessionControllerStatusNonWorking(t *testing.T) {
	for _, tc := range []struct {
		wahaStatus string
		want       ports.SessionStatus
	}{
		{"SCAN_QR_CODE", ports.SessionNeedsQR},
		{"STARTING", ports.SessionStarting},
		{"STOPPED", ports.SessionStopped},
		{"FAILED", ports.SessionFailed},
		{"some-unrecognized-future-value", ports.SessionFailed}, // fail-safe default, not a panic or silent WORKING
	} {
		t.Run(tc.wahaStatus, func(t *testing.T) {
			ctrl := newStatusServer(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"name":"session","status":"` + tc.wahaStatus + `"}`))
			})
			status, err := ctrl.Status(context.Background(), wahaConnection())
			if err != nil || status != tc.want {
				t.Fatalf("status=%q err=%v, want %q", status, err, tc.want)
			}
		})
	}
}

func TestSessionControllerStatusProviderUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // guarantees connection refused, not a slow/flaky timeout
	client, err := waha.NewClient(url, "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	provider, err := waha.NewProvider(client)
	if err != nil {
		t.Fatal(err)
	}
	ctrl := waha.NewSessionController(provider)
	status, err := ctrl.Status(context.Background(), wahaConnection())
	if err == nil {
		t.Fatalf("expected an error for an unreachable provider, got status=%q", status)
	}
	if !errors.Is(err, waha.ErrProviderUnavailable) && !errors.Is(err, waha.ErrTransient) {
		t.Fatalf("expected ErrProviderUnavailable/ErrTransient, got %v", err)
	}
}

func TestSessionControllerStatusMalformedResponse(t *testing.T) {
	ctrl := newStatusServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	})
	status, err := ctrl.Status(context.Background(), wahaConnection())
	if err == nil {
		t.Fatalf("expected a decode error, got status=%q", status)
	}
	if !errors.Is(err, waha.ErrUnknown) {
		t.Fatalf("expected ErrUnknown for a malformed response, got %v", err)
	}
}

func TestSessionControllerStatusAuthenticationFailure(t *testing.T) {
	ctrl := newStatusServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	status, err := ctrl.Status(context.Background(), wahaConnection())
	if err == nil {
		t.Fatalf("expected an authentication error, got status=%q", status)
	}
	if !errors.Is(err, waha.ErrAuthentication) {
		t.Fatalf("expected ErrAuthentication, got %v", err)
	}
}
