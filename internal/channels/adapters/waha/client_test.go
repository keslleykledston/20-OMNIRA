package waha_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
		if body["session"] != "omnira_conn" || body["chatId"] != "5511999999999@c.us" || body["text"] != "hello" || body["id"] != "reserved-msg-id-1" {
			t.Fatalf("unexpected body: %#v", body)
		}
		_, _ = w.Write([]byte(`{"id":"reserved-msg-id-1"}`))
	}))
	defer srv.Close()

	c, err := waha.NewClient(srv.URL, "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.SendText(context.Background(), "omnira_conn", "5511999999999@c.us", "hello", "reserved-msg-id-1")
	if err != nil || id != "reserved-msg-id-1" {
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

func TestClientDownloadMediaRestrictsOriginAndSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret" {
			t.Fatal("media request missing API key")
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("image-bytes"))
	}))
	defer srv.Close()
	c, err := waha.NewClient(srv.URL, "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	data, contentType, err := c.DownloadMedia(context.Background(), srv.URL+"/api/files/media.jpg")
	if err != nil || string(data) != "image-bytes" || contentType != "image/jpeg" {
		t.Fatalf("unexpected media result: %q %q %v", data, contentType, err)
	}
	if _, _, err := c.DownloadMedia(context.Background(), "https://attacker.example/secret"); !errors.Is(err, waha.ErrMediaSourceNotAllowed) {
		t.Fatalf("external media origin accepted: %v", err)
	}
}

// PILOT.4A1 test A: NewMessageID calls the exact deployed provider endpoint
// (read-only — no send side effect) and parses the response correctly.
func TestClientNewMessageIDCallsExactEndpoint(t *testing.T) {
	var gotMethod, gotPath, gotAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAPIKey = r.Method, r.URL.Path, r.Header.Get("X-Api-Key")
		_, _ = w.Write([]byte(`{"id":"3EB0D8E5188C881CA54F3A"}`))
	}))
	defer srv.Close()
	c, err := waha.NewClient(srv.URL, "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.NewMessageID(context.Background(), "omnira_conn")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/omnira_conn/new-message-id" {
		t.Fatalf("unexpected request: %s %s", gotMethod, gotPath)
	}
	if gotAPIKey != "secret" {
		t.Fatal("new-message-id request missing API key")
	}
	if id != "3EB0D8E5188C881CA54F3A" {
		t.Fatalf("unexpected id: %q", id)
	}
}

// PILOT.4A1 test B: an empty generated id is rejected, never silently
// treated as a usable reservation.
func TestClientNewMessageIDRejectsEmptyID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":""}`))
	}))
	defer srv.Close()
	c, err := waha.NewClient(srv.URL, "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.NewMessageID(context.Background(), "omnira_conn"); !errors.Is(err, waha.ErrUnknown) {
		t.Fatalf("empty id accepted: %v", err)
	}
}

// PILOT.4A1 test C/D: SendText forwards the exact supplied id verbatim as
// the JSON "id" property — never generating or transforming it itself.
func TestClientSendTextForwardsSuppliedIDVerbatim(t *testing.T) {
	const suppliedID = "CALLER-SUPPLIED-STABLE-ID-001"
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"` + gotBody["id"] + `"}`))
	}))
	defer srv.Close()
	c, err := waha.NewClient(srv.URL, "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.SendText(context.Background(), "omnira_conn", "5511999999999@c.us", "hello", suppliedID)
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["id"] != suppliedID {
		t.Fatalf("request id = %q, want the exact supplied value %q", gotBody["id"], suppliedID)
	}
	if id != suppliedID {
		t.Fatalf("returned id = %q, want %q", id, suppliedID)
	}
}

// PILOT.4A2: an HTTP 2xx response with an empty message id is not silently
// treated as success — an id is always part of WAHA/GOWS's documented
// contract (PILOT.4A0), so a missing one is a malformed response, not
// evidence either way. Classified as ErrUnknown (safe to retry with the
// same reserved id — the delivery layer decides what to do on exhaustion).
func TestClientSendTextRejectsEmptyResponseID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":""}`))
	}))
	defer srv.Close()
	c, err := waha.NewClient(srv.URL, "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SendText(context.Background(), "omnira_conn", "5511999999999@c.us", "hello", "reserved-id-1"); !errors.Is(err, waha.ErrUnknown) {
		t.Fatalf("empty response id accepted or misclassified: %v", err)
	}
}

// PILOT.4A1 test E: neither the API key nor any request/response detail
// leaks into an error message.
func TestClientErrorsNeverContainAPIKey(t *testing.T) {
	const secretKey = "super-secret-waha-key-should-never-leak"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c, err := waha.NewClient(srv.URL, secretKey, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.NewMessageID(context.Background(), "omnira_conn")
	if err == nil {
		t.Fatal("expected an error from a 500 response")
	}
	if strings.Contains(err.Error(), secretKey) {
		t.Fatalf("error message leaked the API key: %v", err)
	}
	_, sendErr := c.SendText(context.Background(), "omnira_conn", "5511999999999@c.us", "hello", "some-id")
	if sendErr == nil || strings.Contains(sendErr.Error(), secretKey) {
		t.Fatalf("sendText error leaked the API key: %v", sendErr)
	}
}

// GOWS lists a session's groups with capitalised keys and every participant. Only the id, the name
// and the participant count are kept (ADR-0015), and a failing provider is reported, not swallowed.
func TestClientListGroupsKeepsOnlyWhatTheAdminNeeds(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`[
		  {"JID":"120363000000000001@g.us","Name":"Oficial","ParticipantCount":14,"OwnerJID":"x@lid","Participants":[{"JID":"1@lid","PhoneNumber":"5511999999999@s.whatsapp.net"}]},
		  {"JID":"120363000000000002@g.us","Name":"","ParticipantCount":0,"Participants":[]}
		]`))
	}))
	defer srv.Close()
	client := mustClient(t, srv.URL)
	groups, err := client.ListGroups(context.Background(), "omnira_conn")
	if err != nil || path != "/api/omnira_conn/groups" {
		t.Fatalf("path=%q err=%v", path, err)
	}
	if len(groups) != 2 || groups[0].JID != "120363000000000001@g.us" || groups[0].Name != "Oficial" || groups[0].ParticipantCount != 14 || groups[1].Name != "" {
		t.Fatalf("groups = %+v", groups)
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusBadGateway) }))
	defer down.Close()
	if _, err := mustClient(t, down.URL).ListGroups(context.Background(), "omnira_conn"); err == nil {
		t.Fatal("a failing provider must be an error")
	}
}
