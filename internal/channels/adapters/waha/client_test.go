package waha_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnira/omnira/internal/channels/adapters/waha"
)

func TestClientCreateSessionWithWebhookConfiguresOnlyAllowedEvents(t *testing.T) {
	const secret = "webhook-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name   string `json:"name"`
			Start  bool   `json:"start"`
			Config struct {
				Webhooks []struct {
					URL    string   `json:"url"`
					Events []string `json:"events"`
					HMAC   struct {
						Key string `json:"key"`
					} `json:"hmac"`
				} `json:"webhooks"`
			} `json:"config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Start || len(body.Config.Webhooks) != 1 || body.Config.Webhooks[0].HMAC.Key != secret {
			t.Fatalf("unexpected session config: %+v", body)
		}
		if got := body.Config.Webhooks[0].Events; len(got) != 3 || got[0] != "message.any" || got[1] != "message.ack" || got[2] != "session.status" {
			t.Fatalf("unexpected webhook events: %#v", got)
		}
		_, _ = w.Write([]byte(`{"name":"session","status":"STOPPED"}`))
	}))
	defer srv.Close()
	client, err := waha.NewClient(srv.URL, "api-key", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateSessionWithWebhook(context.Background(), "omnira_conn", "https://omnira.example/webhook", secret); err != nil {
		t.Fatal(err)
	}
}

func TestClientHealthUsesAPIKeyWithoutExposingIt(t *testing.T) {
	const secret = "waha-secret-not-for-logs"
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c, err := waha.NewClient(srv.URL, secret, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotKey != secret {
		t.Fatal("client did not authenticate request")
	}
}

func TestClientMapsProviderErrors(t *testing.T) {
	for _, tc := range []struct {
		code int
		want error
	}{{401, waha.ErrAuthentication}, {429, waha.ErrTransient}, {400, waha.ErrPermanent}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code) }))
		c, err := waha.NewClient(srv.URL, "secret", srv.Client())
		if err != nil {
			t.Fatal(err)
		}
		err = c.Health(context.Background())
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: got %v", tc.code, err)
		}
		srv.Close()
	}
}

func TestClientRejectsMissingConfiguration(t *testing.T) {
	if _, err := waha.NewClient("", "secret", nil); !errors.Is(err, waha.ErrConfiguration) {
		t.Fatal("empty URL accepted")
	}
	if _, err := waha.NewClient("http://waha:3000", "", nil); !errors.Is(err, waha.ErrConfiguration) {
		t.Fatal("empty API key accepted")
	}
}

func TestClientSendTextUsesCanonicalWAHABody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sendText" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["session"] != "omnira_conn" || body["chatId"] != "5511999999999@c.us" || body["text"] != "hello" {
			t.Fatalf("unexpected body: %#v", body)
		}
		_, _ = w.Write([]byte(`{"id":"false_5511999999999@c.us_abc"}`))
	}))
	defer srv.Close()

	c, err := waha.NewClient(srv.URL, "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.SendText(context.Background(), "omnira_conn", "5511999999999@c.us", "hello")
	if err != nil || id != "false_5511999999999@c.us_abc" {
		t.Fatalf("unexpected result: %q, %v", id, err)
	}
}

func TestClientClassifiesNonRetryableSessionAndProviderFailures(t *testing.T) {
	for _, tc := range []struct {
		code int
		want error
	}{
		{http.StatusConflict, waha.ErrSessionDisconnected},
		{http.StatusInternalServerError, waha.ErrProviderUnavailable},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code) }))
		c, err := waha.NewClient(srv.URL, "secret", srv.Client())
		if err != nil {
			t.Fatal(err)
		}
		err = c.Health(context.Background())
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: got %v", tc.code, err)
		}
		srv.Close()
	}
}
