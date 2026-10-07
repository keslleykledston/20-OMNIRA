package meta_test

import (
	"context"
	"encoding/json"
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
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?hub.mode=subscribe&hub.verify_token=omn-good&hub.challenge=%3Cb%3E1", nil))
	if rec.Body.String() != "<b>1" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("challenge must be the raw value as text/plain+nosniff: %q %v", rec.Body.String(), rec.Header())
	}
	if get("omn-good") != 200 || get("nope") != 403 {
		t.Fatal("challenge must pass only for a valid per-connection token")
	}
}

func TestListTemplatesParsesBodyVariablesAndFlagsWhatWeCannotFill(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v21.0/109876543210987/message_templates" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[
		 {"id":"1","name":"boas_vindas","language":"pt_BR","category":"UTILITY","status":"APPROVED","components":[{"type":"BODY","text":"Olá {{1}}, seu chamado {{2}} foi aberto."}]},
		 {"id":"2","name":"com_imagem","language":"pt_BR","category":"MARKETING","status":"APPROVED","components":[{"type":"HEADER","format":"IMAGE"},{"type":"BODY","text":"Promo"}]},
		 {"id":"3","name":"com_link","language":"pt_BR","category":"UTILITY","status":"PENDING","components":[{"type":"BODY","text":"Veja"},{"type":"BUTTONS","buttons":[{"type":"URL","url":"https://x.com/{{1}}"}]}]}
		]}`))
	})
	got, err := c.ListTemplates(context.Background(), "tok", "109876543210987")
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].VariableCount != 2 || got[0].UnsupportedReason != "" || got[0].Body == "" {
		t.Fatalf("plain template: %+v", got[0])
	}
	if got[1].UnsupportedReason == "" || got[2].UnsupportedReason == "" {
		t.Fatalf("header media and dynamic url must be flagged: %+v %+v", got[1], got[2])
	}
}

func TestSendTemplateBuildsBodyParametersAndClassifiesLikeText(t *testing.T) {
	var sent map[string]any
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.T"}]}`))
	})
	id, err := c.SendTemplate(context.Background(), "tok", "12345678", "5592984517378", "boas_vindas", "pt_BR", []string{"Ana", "123"})
	if err != nil || id != "wamid.T" {
		t.Fatalf("%q %v", id, err)
	}
	tpl := sent["template"].(map[string]any)
	comp := tpl["components"].([]any)[0].(map[string]any)
	if sent["type"] != "template" || tpl["name"] != "boas_vindas" || comp["type"] != "body" || len(comp["parameters"].([]any)) != 2 {
		t.Fatalf("payload: %v", sent)
	}
	c2, _ := newClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	if _, err := c2.SendTemplate(context.Background(), "tok", "12345678", "5592984517378", "boas_vindas", "pt_BR", nil); !errors.Is(err, ports.ErrOutcomeUnknown) {
		t.Fatalf("a 5xx on a template send is an unknown outcome, got %v", err)
	}
}

func TestSendInteractiveBuildsButtonsOrAListAndRefusesWhatDoesNotFit(t *testing.T) {
	var sent map[string]any
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.I"}]}`))
	})
	opts := func(titles ...string) []domain.InteractiveOption {
		var o []domain.InteractiveOption
		for i, t := range titles {
			o = append(o, domain.InteractiveOption{ID: "o" + string(rune('a'+i)), Title: t})
		}
		return o
	}
	msg := domain.OutboundInteractiveMessage{Body: "Como ajudar?", Options: opts("Suporte", "Financeiro")}
	if id, err := c.SendInteractive(context.Background(), "tok", "12345678", "5592984517378", msg); err != nil || id != "wamid.I" {
		t.Fatalf("%q %v", id, err)
	}
	it := sent["interactive"].(map[string]any)
	if sent["type"] != "interactive" || it["type"] != "button" || len(it["action"].(map[string]any)["buttons"].([]any)) != 2 {
		t.Fatalf("2 options must be reply buttons: %v", sent)
	}
	msg.Options = opts("Suporte", "Financeiro", "Comercial", "Outro assunto")
	if _, err := c.SendInteractive(context.Background(), "tok", "12345678", "5592984517378", msg); err != nil {
		t.Fatal(err)
	}
	it = sent["interactive"].(map[string]any)
	if it["type"] != "list" || len(it["action"].(map[string]any)["sections"].([]any)[0].(map[string]any)["rows"].([]any)) != 4 {
		t.Fatalf("4 options must be a list: %v", sent)
	}
	msg.Options = opts("Um título bem grande demais", "B")
	if _, err := c.SendInteractive(context.Background(), "tok", "12345678", "5592984517378", msg); !errors.Is(err, ports.ErrPermanent) {
		t.Fatalf("a title over 20 characters must be refused, not truncated: %v", err)
	}
}
