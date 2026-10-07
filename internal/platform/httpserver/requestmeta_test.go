package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func serve(t *testing.T, h http.Handler, accept, clientID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/x", nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if clientID != "" {
		req.Header.Set("X-Request-ID", clientID)
	}
	rr := httptest.NewRecorder()
	RequestMeta(h).ServeHTTP(rr, req)
	return rr
}

var errHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "conversation not found", http.StatusNotFound)
})

func TestEveryResponseCarriesARequestID(t *testing.T) {
	rr := serve(t, errHandler, "", "")
	if id := rr.Header().Get("X-Request-ID"); len(id) != 36 {
		t.Fatalf("generated id = %q, want a uuid", id)
	}
	// without opting in, the body is exactly what http.Error wrote: the web is unaffected
	if rr.Code != 404 || rr.Body.String() != "conversation not found\n" || !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("legacy error changed: %d %q %q", rr.Code, rr.Body.String(), rr.Header().Get("Content-Type"))
	}
	if serve(t, errHandler, "", "").Header().Get("X-Request-ID") == rr.Header().Get("X-Request-ID") {
		t.Fatal("two requests must not share a generated id")
	}
}

func TestClientRequestIDIsEchoedOnlyWhenItIsAPlainToken(t *testing.T) {
	if got := serve(t, errHandler, "", "mobile-7f3a9c21").Header().Get("X-Request-ID"); got != "mobile-7f3a9c21" {
		t.Fatalf("a plain client id must be echoed, got %q", got)
	}
	for _, bad := range []string{"short", strings.Repeat("a", 65), "has space in it", "a\r\nSet-Cookie: x=1", "../../etc/passwd", "<script>alert(1)</script>"} {
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header["X-Request-Id"] = []string{bad} // bypass header validation of the test client
		rr := httptest.NewRecorder()
		RequestMeta(errHandler).ServeHTTP(rr, req)
		got := rr.Header().Get("X-Request-ID")
		if got == bad || len(got) != 36 {
			t.Fatalf("unsafe client id %q was echoed as %q", bad, got)
		}
	}
}

func TestOptInErrorEnvelope(t *testing.T) {
	for status, code := range map[int]string{400: "INVALID_REQUEST", 401: "UNAUTHENTICATED", 403: "FORBIDDEN", 404: "NOT_FOUND", 409: "CONFLICT", 413: "PAYLOAD_TOO_LARGE", 429: "RATE_LIMITED", 500: "INTERNAL", 503: "UNAVAILABLE", 418: "REJECTED"} {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "some human text", status) })
		rr := serve(t, h, "application/json, "+MediaTypeV1, "req-abcdef12")
		if rr.Code != status || rr.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%d: status/content-type = %d %q", status, rr.Code, rr.Header().Get("Content-Type"))
		}
		var env errorEnvelope
		if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
			t.Fatalf("%d: not json: %v (%q)", status, err, rr.Body.String())
		}
		if env.Error.Code != code || env.Error.Message != "some human text" || env.Error.RequestID != "req-abcdef12" || rr.Header().Get("X-Request-ID") != "req-abcdef12" {
			t.Fatalf("%d: envelope = %+v", status, env)
		}
	}
}

func TestEnvelopeLeavesEverythingElseUntouched(t *testing.T) {
	opt := MediaTypeV1
	// success bodies
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"id":"1"}`))
	})
	if rr := serve(t, ok, opt, ""); rr.Code != 202 || rr.Body.String() != `{"id":"1"}` {
		t.Fatalf("success changed: %d %q", rr.Code, rr.Body.String())
	}
	// errors that already carry JSON keep their own body
	jerr := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"code":"MINE"}`))
	})
	if rr := serve(t, jerr, opt, ""); rr.Code != 409 || rr.Body.String() != `{"code":"MINE"}` {
		t.Fatalf("a JSON error must pass through: %d %q", rr.Code, rr.Body.String())
	}
	// an implicit 200 text/plain body is not an error
	plain := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	if rr := serve(t, plain, opt, ""); rr.Code != 200 || rr.Body.String() != "ok" {
		t.Fatalf("plain 200 changed: %d %q", rr.Code, rr.Body.String())
	}
	// headers set by the handler survive the rewrite (Retry-After is part of the 429 contract)
	limited := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "too many realtime connections", 429)
	})
	if rr := serve(t, limited, opt, ""); rr.Header().Get("Retry-After") != "5" || rr.Code != 429 {
		t.Fatalf("Retry-After lost: %v", rr.Header())
	}
	// a huge error body is bounded and the envelope message too
	big := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, strings.Repeat("x", 100000), 500) })
	rr := serve(t, big, opt, "")
	var env errorEnvelope
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if len(env.Error.Message) != 300 {
		t.Fatalf("message length = %d, want 300", len(env.Error.Message))
	}
}

// deadlineRecorder is a ResponseWriter that, unlike httptest.ResponseRecorder, supports http.ResponseController.SetWriteDeadline
// (what the SSE handler calls to lift the server-wide write timeout), and records that it was reached.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlineSet bool
}

func (d *deadlineRecorder) SetWriteDeadline(time.Time) error { d.deadlineSet = true; return nil }

// A stream must keep flowing: the writer wrapper must not hold back or break Flush and ResponseController.
func TestEnvelopeDoesNotBreakStreaming(t *testing.T) {
	streaming := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
			t.Errorf("ResponseController must reach the real writer through the wrapper: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": connected\n\n"))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("data: {}\n\n"))
	})
	base := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest("GET", "/stream", nil)
	req.Header.Set("Accept", MediaTypeV1)
	RequestMeta(streaming).ServeHTTP(base, req)
	if !base.deadlineSet {
		t.Fatal("SetWriteDeadline never reached the real writer: a stream would be cut by the server write timeout")
	}
	if !base.Flushed || base.Body.String() != ": connected\n\ndata: {}\n\n" || base.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream altered: flushed=%v body=%q ct=%q", base.Flushed, base.Body.String(), base.Header().Get("Content-Type"))
	}
}

func TestRequestIDIsAvailableToHandlers(t *testing.T) {
	var seen string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = RequestIDFromContext(r.Context()) })
	rr := serve(t, h, "", "")
	if seen == "" || seen != rr.Header().Get("X-Request-ID") {
		t.Fatalf("context id %q != header %q", seen, rr.Header().Get("X-Request-ID"))
	}
	if RequestIDFromContext(httptest.NewRequest("GET", "/", nil).Context()) != "" {
		t.Fatal("no id outside RequestMeta")
	}
}

// The mobile client will send the vendor Accept on EVERY request, media downloads included. Range (audio seeking), 304 and HEAD must
// behave exactly as without the wrapper.
func TestEnvelopeKeepsRangeConditionalAndHeadResponsesIntact(t *testing.T) {
	content := strings.Repeat("0123456789", 100)
	modTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	media := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/ogg")
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "a.ogg", modTime, strings.NewReader(content))
	})
	do := func(method string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/media", nil)
		req.Header.Set("Accept", MediaTypeV1)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rr := httptest.NewRecorder()
		RequestMeta(media).ServeHTTP(rr, req)
		return rr
	}
	if rr := do("GET", map[string]string{"Range": "bytes=10-19"}); rr.Code != 206 || rr.Body.String() != "0123456789" || rr.Header().Get("Content-Range") != "bytes 10-19/1000" {
		t.Fatalf("range: %d %q %q", rr.Code, rr.Body.String(), rr.Header().Get("Content-Range"))
	}
	if rr := do("GET", map[string]string{"If-None-Match": `"v1"`}); rr.Code != 304 || rr.Body.Len() != 0 {
		t.Fatalf("conditional: %d %q", rr.Code, rr.Body.String())
	}
	if rr := do("HEAD", nil); rr.Code != 200 || rr.Body.Len() != 0 || rr.Header().Get("Content-Length") != "1000" {
		t.Fatalf("head: %d len=%d cl=%q", rr.Code, rr.Body.Len(), rr.Header().Get("Content-Length"))
	}
	// an unsatisfiable range is an ERROR written by ServeContent as text/plain: it becomes the envelope, with its status kept
	if rr := do("GET", map[string]string{"Range": "bytes=5000-6000"}); rr.Code != 416 || !strings.Contains(rr.Body.String(), `"code":"REJECTED"`) {
		t.Fatalf("416: %d %q", rr.Code, rr.Body.String())
	}
}

func TestEnvelopeSurvivesAHandlerThatWritesTheHeaderTwiceOrAfterTheBody(t *testing.T) {
	twice := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "first", 404)
		w.WriteHeader(500) // superfluous, must not corrupt the already captured error
		_, _ = w.Write([]byte("late"))
	})
	rr := serve(t, twice, MediaTypeV1, "")
	var env errorEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil || rr.Code != 404 || env.Error.Code != "NOT_FOUND" {
		t.Fatalf("%d %q (%v)", rr.Code, rr.Body.String(), err)
	}
}

func TestInterimResponsesAreNotTheFinalStatusAndCachesKeyOnAccept(t *testing.T) {
	early := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", "</a.css>; rel=preload")
		w.WriteHeader(http.StatusEarlyHints)
		http.Error(w, "gone", http.StatusNotFound)
	})
	srv := httptest.NewServer(RequestMeta(early))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Accept", MediaTypeV1)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env errorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil || resp.StatusCode != 404 || env.Error.Code != "NOT_FOUND" {
		t.Fatalf("a 1xx must not swallow the final error: %d %+v (%v)", resp.StatusCode, env, err)
	}
	if v := resp.Header.Values("Vary"); len(v) == 0 || !strings.Contains(strings.Join(v, ","), "Accept") {
		t.Fatalf("Vary: Accept missing: %v", v)
	}
}
