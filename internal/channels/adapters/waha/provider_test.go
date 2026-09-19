package waha_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/adapters/waha"
	"github.com/omnira/omnira/internal/channels/domain"
)

func TestProviderSessionLifecycleAndPairing(t *testing.T) {
	connection := wahaConnection()
	var requested []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret" {
			t.Error("missing API key")
		}
		requested = append(requested, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/sessions":
			var body struct {
				Name  string `json:"name"`
				Start bool   `json:"start"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Name != "omnira_"+connection.ID.String() || body.Start {
				t.Errorf("unsafe create request: %+v", body)
			}
		case "/api/" + "omnira_" + connection.ID.String() + "/auth/qr":
			if r.Header.Get("Accept") != "application/json" {
				t.Error("QR request did not ask for JSON")
			}
			_, _ = w.Write([]byte(`{"mimetype":"image/png","data":"qr-base64"}`))
			return
		case "/api/sessions/" + "omnira_" + connection.ID.String() + "/me":
			_, _ = w.Write([]byte(`{"id":"5511999999999@c.us","pushName":"Omnira"}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"session","status":"WORKING"}`))
	}))
	defer srv.Close()

	client, err := waha.NewClient(srv.URL, "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	provider, err := waha.NewProvider(client)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = provider.CreateSession(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.StartSession(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.StopSession(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.RestartSession(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.GetSession(ctx, connection); err != nil {
		t.Fatal(err)
	}
	qr, err := provider.GetQRCode(ctx, connection)
	if err != nil || qr.Data != "qr-base64" || qr.MIMEType != "image/png" {
		t.Fatalf("unexpected QR: %+v, err=%v", qr, err)
	}
	me, err := provider.GetMe(ctx, connection)
	if err != nil || me == nil || me.ID != "5511999999999@c.us" {
		t.Fatalf("unexpected account: %+v, err=%v", me, err)
	}

	joined := strings.Join(requested, "\n")
	for _, path := range []string{"POST /api/sessions", "POST /api/sessions/" + "omnira_" + connection.ID.String() + "/start", "POST /api/sessions/" + "omnira_" + connection.ID.String() + "/stop", "POST /api/sessions/" + "omnira_" + connection.ID.String() + "/restart", "GET /api/sessions/" + "omnira_" + connection.ID.String(), "GET /api/" + "omnira_" + connection.ID.String() + "/auth/qr", "GET /api/sessions/" + "omnira_" + connection.ID.String() + "/me"} {
		if !strings.Contains(joined, path) {
			t.Errorf("missing request %q in:\n%s", path, joined)
		}
	}
}

func TestProviderSessionOwnershipAndTenantSeparation(t *testing.T) {
	first := wahaConnection()
	second := wahaConnection()
	second.TenantID = uuid.New()
	client, err := waha.NewClient("http://waha:3000", "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := waha.NewProvider(client)
	if err != nil {
		t.Fatal(err)
	}
	one, err := provider.SessionRef(first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := provider.SessionRef(second)
	if err != nil {
		t.Fatal(err)
	}
	if one == two || !strings.HasPrefix(one, "omnira_") || !strings.HasPrefix(two, "omnira_") {
		t.Fatalf("session collision or unsafe prefix: %q %q", one, two)
	}
	first.ProviderSessionRef = "user-chosen"
	if _, err := provider.SessionRef(first); err == nil {
		t.Fatal("arbitrary provider session reference accepted")
	}
}

func TestCanonicalStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		waha string
		want domain.ConnectionStatus
	}{
		{"STOPPED", domain.ConnectionStatusDisconnected},
		{"STARTING", domain.ConnectionStatusPending},
		{"SCAN_QR_CODE", domain.ConnectionStatusPending},
		{"WORKING", domain.ConnectionStatusActive},
		{"FAILED", domain.ConnectionStatusFailed},
		{"new-provider-state", domain.ConnectionStatusDegraded},
	} {
		if got := waha.CanonicalStatus(tc.waha); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.waha, got, tc.want)
		}
	}
}

func wahaConnection() domain.ChannelConnection {
	return domain.ChannelConnection{
		ID:           uuid.New(),
		TenantID:     uuid.New(),
		Channel:      domain.ChannelWhatsApp,
		Provider:     domain.ProviderWAHA,
		ProviderKind: domain.ProviderKindUnofficial,
		Status:       domain.ConnectionStatusPending,
	}
}
