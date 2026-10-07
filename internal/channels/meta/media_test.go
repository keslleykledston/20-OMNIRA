package meta_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnira/omnira/internal/channels/ports"
)

func TestUploadMediaSendsAMultipartFileAndReturnsTheMediaID(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v21.0/12345678/media" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("path=%s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("messaging_product") != "whatsapp" || r.FormValue("type") != "application/pdf" {
			t.Errorf("form: %v", r.MultipartForm.Value)
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(f)
		if string(b) != "%PDF-1.4" || hdr.Filename != "boleto.pdf" || hdr.Header.Get("Content-Type") != "application/pdf" {
			t.Errorf("file=%q name=%q type=%q", b, hdr.Filename, hdr.Header.Get("Content-Type"))
		}
		_, _ = w.Write([]byte(`{"id":"MEDIA1"}`))
	})
	id, err := c.UploadMedia(context.Background(), "tok", "12345678", "application/pdf", "boleto.pdf", []byte("%PDF-1.4"))
	if err != nil || id != "MEDIA1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestAFailedUploadIsAlwaysSafeToRepeatExceptWhenRejected(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   error
		not    error
	}{
		"server error becomes retryable": {500, `{}`, ports.ErrTransient, ports.ErrOutcomeUnknown},
		"throttle":                       {429, `{"error":{"code":4}}`, ports.ErrRateLimited, nil},
		"auth":                           {401, `{"error":{"code":190}}`, ports.ErrAuthentication, nil},
		"rejected file":                  {400, `{"error":{"code":100}}`, ports.ErrPermanent, nil},
	} {
		c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		})
		_, err := c.UploadMedia(context.Background(), "tok", "12345678", "image/png", "a.png", []byte("x"))
		if !errors.Is(err, tc.want) || (tc.not != nil && errors.Is(err, tc.not)) {
			t.Errorf("%s: %v", name, err)
		}
	}
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	if _, err := c.UploadMedia(context.Background(), "tok", "12345678", "image/png", "a.png", []byte("x")); !errors.Is(err, ports.ErrTransient) {
		t.Errorf("accepted without a media id: %v", err)
	}
}

func TestSendMediaBuildsTheMessagePerKindAndKeepsTheNoRetryRuleForAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		kind, caption, name string
		wantKeys            []string
		notKeys             []string
	}{
		{"image", "foto", "", []string{"id", "caption"}, []string{"filename"}},
		{"video", "clip", "", []string{"id", "caption"}, nil},
		{"audio", "ignored", "", []string{"id"}, []string{"caption", "filename"}},
		{"document", "boleto", "boleto.pdf", []string{"id", "caption", "filename"}, nil},
	} {
		var body map[string]any
		c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.M"}]}`))
		})
		id, err := c.SendMedia(context.Background(), "tok", "12345678", "5592984517378", tc.kind, "MEDIA1", tc.caption, tc.name)
		if err != nil || id != "wamid.M" {
			t.Fatalf("%s: id=%q err=%v", tc.kind, id, err)
		}
		media, _ := body[tc.kind].(map[string]any)
		if body["type"] != tc.kind || body["to"] != "5592984517378" || media == nil {
			t.Fatalf("%s: %v", tc.kind, body)
		}
		for _, k := range tc.wantKeys {
			if _, ok := media[k]; !ok {
				t.Errorf("%s: missing %s in %v", tc.kind, k, media)
			}
		}
		for _, k := range tc.notKeys {
			if _, ok := media[k]; ok {
				t.Errorf("%s: unexpected %s in %v", tc.kind, k, media)
			}
		}
	}
	// ambiguity on the MESSAGE step is never retryable
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	if _, err := c.SendMedia(context.Background(), "tok", "12345678", "5592984517378", "image", "MEDIA1", "", ""); !errors.Is(err, ports.ErrOutcomeUnknown) {
		t.Errorf("5xx on send: %v", err)
	}
	c, _ = newClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	if _, err := c.SendMedia(context.Background(), "tok", "12345678", "5592984517378", "image", "MEDIA1", "", ""); !errors.Is(err, ports.ErrOutcomeUnknown) {
		t.Errorf("no id on send: %v", err)
	}
	if _, err := c.SendMedia(context.Background(), "tok", "12345678", "5592984517378", "sticker", "MEDIA1", "", ""); !errors.Is(err, ports.ErrPermanent) {
		t.Errorf("unsupported kind: %v", err)
	}
}

func TestUploadMediaNeverFollowsARedirect(t *testing.T) {
	var leaked int
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked++ }))
	defer evil.Close()
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL, http.StatusTemporaryRedirect)
	})
	if _, err := c.UploadMedia(context.Background(), "tok", "12345678", "image/png", "a.png", []byte("x")); err == nil || leaked != 0 {
		t.Fatalf("err=%v leaked=%d", err, leaked)
	}
}
