package adapters

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	mediaports "github.com/omnira/omnira/internal/media/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type memFile struct{ *bytes.Reader }

func (memFile) Close() error { return nil }

type fakeReader struct {
	media *mediaports.ServedMedia
	err   error
}

func (f fakeReader) Open(context.Context, uuid.UUID, uuid.UUID) (*mediaports.ServedMedia, error) {
	return f.media, f.err
}

func serve(t *testing.T, rd fakeReader, rangeHeader string) *httptest.ResponseRecorder {
	t.Helper()
	tenant, user, message := uuid.New(), uuid.New(), uuid.New()
	tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/media", nil).WithContext(tenancydomain.WithTenantContext(context.Background(), tc))
	req.SetPathValue("message_id", message.String())
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	rec := httptest.NewRecorder()
	(&InboxAPIHandler{}).WithMediaReader(rd).GetMedia(rec, req)
	return rec
}

func cleanMedia(mime string, data []byte) *mediaports.ServedMedia {
	return &mediaports.ServedMedia{Status: "clean", Mime: mime, Size: int64(len(data)), SHA256: "abc123", File: memFile{bytes.NewReader(data)}}
}

func TestStoredMediaOnlyServesClearedFilesAndSaysWhyOtherwise(t *testing.T) {
	cases := []struct {
		status string
		want   int
	}{
		{"pending", http.StatusConflict}, {"quarantined", http.StatusConflict},
		{"infected", http.StatusForbidden}, {"rejected", http.StatusForbidden}, {"failed", http.StatusForbidden},
		{"source_gone", http.StatusGone}, {"purged", http.StatusGone},
	}
	for _, c := range cases {
		rec := serve(t, fakeReader{media: &mediaports.ServedMedia{Status: c.status, Mime: "image/png"}}, "")
		if rec.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.status, rec.Code, c.want)
		}
		if rec.Body.Len() > 0 && !strings.Contains(rec.Body.String(), "media") {
			t.Errorf("%s: unexpected body", c.status)
		}
	}
	if rec := serve(t, fakeReader{err: mediaports.ErrMediaNotFound}, ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown message: %d, want 404", rec.Code)
	}
}

func TestStoredMediaHeadersContainTheBrowser(t *testing.T) {
	rec := serve(t, fakeReader{media: cleanMedia("image/png", []byte("PNGDATA"))}, "")
	h := rec.Header()
	if rec.Code != 200 || h.Get("Content-Type") != "image/png" || h.Get("Content-Disposition") != "inline" {
		t.Fatalf("image: %d %q %q", rec.Code, h.Get("Content-Type"), h.Get("Content-Disposition"))
	}
	for k, v := range map[string]string{"X-Content-Type-Options": "nosniff", "Cross-Origin-Resource-Policy": "same-origin"} {
		if h.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, h.Get(k), v)
		}
	}
	if !strings.HasPrefix(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("CSP must sandbox the response, got %q", h.Get("Content-Security-Policy"))
	}
}

func TestStoredMediaDocumentsAreAlwaysDownloadsNeverRenderedInline(t *testing.T) {
	for _, mime := range []string{"application/pdf", "text/plain", "application/octet-stream", "text/html", "image/svg+xml"} {
		rec := serve(t, fakeReader{media: cleanMedia(mime, []byte("data"))}, "")
		h := rec.Header()
		if h.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(h.Get("Content-Disposition"), "attachment;") {
			t.Errorf("%s served as %q / %q; must be an octet-stream attachment", mime, h.Get("Content-Type"), h.Get("Content-Disposition"))
		}
	}
}

func TestStoredMediaSupportsRangeSoAudioCanSeek(t *testing.T) {
	rec := serve(t, fakeReader{media: cleanMedia("audio/ogg", []byte("0123456789"))}, "bytes=2-5")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "2345" {
		t.Fatalf("range: %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "audio/ogg" {
		t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestImagesAndAudioAreCachedPrivatelyForADayEverythingElseIsNot(t *testing.T) {
	for _, c := range []struct {
		mime  string
		cache string
	}{
		{"image/png", "private, max-age=86400"}, {"audio/ogg", "private, max-age=86400"},
		{"video/mp4", "private, no-store"}, {"application/pdf", "private, no-store"}, {"text/plain", "private, no-store"},
	} {
		rec := serve(t, fakeReader{media: cleanMedia(c.mime, []byte("data"))}, "")
		if got := rec.Header().Get("Cache-Control"); got != c.cache {
			t.Errorf("%s: Cache-Control = %q, want %q", c.mime, got, c.cache)
		}
		if (c.cache != "private, no-store") != (rec.Header().Get("ETag") != "") {
			t.Errorf("%s: ETag %q inconsistent with caching", c.mime, rec.Header().Get("ETag"))
		}
	}
	// Never public, never shared: a cacheable response must not carry "public" or "s-maxage".
	rec := serve(t, fakeReader{media: cleanMedia("image/png", []byte("data"))}, "")
	if cc := rec.Header().Get("Cache-Control"); strings.Contains(cc, "public") || strings.Contains(cc, "s-maxage") {
		t.Fatalf("cacheable media must stay private, got %q", cc)
	}
}

func TestRevalidationWithTheEtagSendsNoBody(t *testing.T) {
	first := serve(t, fakeReader{media: cleanMedia("audio/ogg", []byte("0123456789"))}, "")
	etag := first.Header().Get("ETag")
	if etag != `"abc123"` {
		t.Fatalf("ETag = %q", etag)
	}
	tenant, user, message := uuid.New(), uuid.New(), uuid.New()
	tc, _ := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
	req := httptest.NewRequest(http.MethodGet, "/media", nil).WithContext(tenancydomain.WithTenantContext(context.Background(), tc))
	req.SetPathValue("message_id", message.String())
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	(&InboxAPIHandler{}).WithMediaReader(fakeReader{media: cleanMedia("audio/ogg", []byte("0123456789"))}).GetMedia(rec, req)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("revalidation: %d with %d bytes, want 304 and empty body", rec.Code, rec.Body.Len())
	}
}
