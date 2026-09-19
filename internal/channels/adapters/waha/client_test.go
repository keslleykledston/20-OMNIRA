package waha_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnira/omnira/internal/channels/adapters/waha"
)

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
