package waha_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnira/omnira/internal/channels/adapters/waha"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
)

func mediaProvider(t *testing.T, h http.HandlerFunc) (*waha.WahaProvider, domain.ChannelConnection) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	client, err := waha.NewClient(srv.URL, "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	provider, err := waha.NewProvider(client)
	if err != nil {
		t.Fatal(err)
	}
	return provider, wahaConnection()
}

func TestSendMediaPicksTheEndpointAndShipsTheBytesAsBase64(t *testing.T) {
	type call struct {
		path string
		body map[string]any
	}
	for _, tc := range []struct {
		name, kind, mime, endpoint, wantMime string
		caption                              string
	}{
		{"image", "image", "image/png", "/api/sendImage", "image/png", "a legenda"},
		{"video", "video", "video/mp4", "/api/sendVideo", "video/mp4", "a legenda"},
		{"voice note", "audio", "audio/ogg", "/api/sendVoice", "audio/ogg; codecs=opus", "ignored"},
		{"mp3 goes as a file", "audio", "audio/mpeg", "/api/sendFile", "audio/mpeg", ""},
		{"document", "document", "application/pdf", "/api/sendFile", "application/pdf", "boleto"},
	} {
		var got call
		p, conn := mediaProvider(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Api-Key") != "secret" {
				t.Error("missing API key")
			}
			b, _ := io.ReadAll(r.Body)
			got.path = r.URL.Path
			_ = json.Unmarshal(b, &got.body)
			_, _ = w.Write([]byte(`{"id":"true_5511999990000@c.us_ABC"}`))
		})
		res, err := p.SendMedia(context.Background(), conn, domain.OutboundMediaMessage{
			ToE164: "+5511999990000", Kind: domain.MediaKind(tc.kind), Mime: tc.mime, FileName: "arquivo.bin", Data: []byte("conteudo"), Caption: tc.caption})
		if err != nil || res.ProviderMessageID != "true_5511999990000@c.us_ABC" {
			t.Fatalf("%s: res=%+v err=%v", tc.name, res, err)
		}
		file, _ := got.body["file"].(map[string]any)
		if got.path != tc.endpoint || file["mimetype"] != tc.wantMime || file["data"] != base64.StdEncoding.EncodeToString([]byte("conteudo")) {
			t.Errorf("%s: path=%s body=%v", tc.name, got.path, got.body)
		}
		if tc.endpoint == "/api/sendVoice" {
			if _, has := got.body["caption"]; has {
				t.Errorf("%s: a voice note carries no caption", tc.name)
			}
		}
		if got.body["session"] == "" || got.body["chatId"] != "5511999990000@c.us" {
			t.Errorf("%s: addressing wrong: %v", tc.name, got.body)
		}
		if _, has := got.body["id"]; has {
			t.Errorf("%s: media must not send an id field (no dedupe contract)", tc.name)
		}
	}
}

func TestSendMediaAmbiguousFailuresAreOutcomeUnknownAndNeverRetryable(t *testing.T) {
	for name, status := range map[string]int{"server error": 500, "bad gateway": 502} {
		p, conn := mediaProvider(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
		_, err := p.SendMedia(context.Background(), conn, domain.OutboundMediaMessage{ToE164: "+5511999990000", Kind: domain.MediaKindImage, Mime: "image/png", Data: []byte("x")})
		if !errors.Is(err, ports.ErrOutcomeUnknown) {
			t.Errorf("%s: %v, want ErrOutcomeUnknown", name, err)
		}
	}
	// an answer without a message id may have been delivered
	p, conn := mediaProvider(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	if _, err := p.SendMedia(context.Background(), conn, domain.OutboundMediaMessage{ToE164: "+5511999990000", Kind: domain.MediaKindImage, Mime: "image/png", Data: []byte("x")}); !errors.Is(err, ports.ErrOutcomeUnknown) {
		t.Errorf("no id: %v", err)
	}
	// proven rejections keep their meaning
	for name, tc := range map[string]struct {
		status int
		want   error
	}{"throttle": {429, ports.ErrRateLimited}, "auth": {401, ports.ErrAuthentication}, "rejected": {422, ports.ErrPermanent}, "session": {409, ports.ErrSessionDisconnected}} {
		p, conn := mediaProvider(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) })
		_, err := p.SendMedia(context.Background(), conn, domain.OutboundMediaMessage{ToE164: "+5511999990000", Kind: domain.MediaKindImage, Mime: "image/png", Data: []byte("x")})
		if !errors.Is(err, tc.want) || errors.Is(err, ports.ErrOutcomeUnknown) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	// nothing to send
	p, conn = mediaProvider(t, func(w http.ResponseWriter, r *http.Request) { t.Error("provider must not be called") })
	if _, err := p.SendMedia(context.Background(), conn, domain.OutboundMediaMessage{ToE164: "+5511999990000", Kind: domain.MediaKindImage}); !errors.Is(err, ports.ErrPermanent) {
		t.Errorf("empty media: %v", err)
	}
}
