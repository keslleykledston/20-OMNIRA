package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// Client-independent request metadata (MOBILE.READINESS). Two things every non-browser client needs and the web never had to ask for:
//
//   - X-Request-ID on EVERY response (the client's own id when it is a plain token, otherwise a new one), so a support
//     conversation can point at one request ("the send that failed at 14:02") without the server logging payloads.
//   - an OPT-IN stable error envelope. The API's 700+ error sites answer with net/http's text/plain body; changing them would
//     break the web. A client that sends `Accept: application/vnd.omnira.v1+json` gets
//     {"error":{"code","message","request_id"}} instead, only for error responses that were text/plain. The code is derived from
//     the HTTP status and is the stable part of the contract; the message is human text and may change.
//
// Responses that are not errors, errors that already carry a JSON body, and streams (SSE) pass through byte for byte.

// MediaTypeV1 is the vendor media type a client sends in Accept to opt in to the error envelope.
const MediaTypeV1 = "application/vnd.omnira.v1+json"

const requestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// RequestIDFromContext returns the id attached by RequestMeta ("" outside a request served by it).
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// A client-supplied id is echoed only when it cannot carry anything but an identifier (no log injection, bounded size).
var clientRequestIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

// StableErrorCode maps an HTTP status to the stable machine-readable code of the error envelope.
func StableErrorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_REQUEST"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusMethodNotAllowed:
		return "METHOD_NOT_ALLOWED"
	case http.StatusConflict:
		return "CONFLICT"
	case http.StatusRequestEntityTooLarge:
		return "PAYLOAD_TOO_LARGE"
	case http.StatusUnprocessableEntity:
		return "UNPROCESSABLE"
	case http.StatusTooManyRequests:
		return "RATE_LIMITED"
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return "UNAVAILABLE"
	}
	switch {
	case status >= 500:
		return "INTERNAL"
	case status >= 400:
		return "REJECTED"
	}
	return ""
}

// RequestMeta wraps next with the request id and the opt-in error envelope.
func RequestMeta(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !clientRequestIDRE.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set(requestIDHeader, id)
		w.Header().Add("Vary", "Accept") // error bodies differ by Accept (text/plain vs the v1 envelope): shared caches must key on it
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		if !strings.Contains(strings.ToLower(r.Header.Get("Accept")), MediaTypeV1) {
			next.ServeHTTP(w, r)
			return
		}
		mw := &envelopeWriter{ResponseWriter: w, requestID: id}
		next.ServeHTTP(mw, r)
		mw.finish()
	})
}

// envelopeWriter holds back ONLY a text/plain error response (what http.Error writes) so it can be rewritten; everything else is
// forwarded immediately. It keeps Flush and Unwrap so streaming and http.ResponseController keep working through it.
type envelopeWriter struct {
	http.ResponseWriter
	requestID string
	status    int
	capturing bool
	sent      bool
	buf       bytes.Buffer
}

const maxEnvelopeBody = 4 << 10

func (w *envelopeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *envelopeWriter) Flush() {
	if w.capturing {
		return // nothing is sent until the envelope is built
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *envelopeWriter) WriteHeader(code int) {
	if w.sent || w.capturing {
		return
	}
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code) // interim (1xx, e.g. 103) is not the final status
		return
	}
	if code >= 400 && strings.HasPrefix(strings.ToLower(w.Header().Get("Content-Type")), "text/plain") {
		w.capturing, w.status = true, code
		return
	}
	w.sent = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *envelopeWriter) Write(b []byte) (int, error) {
	if w.capturing {
		if room := maxEnvelopeBody - w.buf.Len(); room > 0 {
			if len(b) > room {
				w.buf.Write(b[:room])
			} else {
				w.buf.Write(b)
			}
		}
		return len(b), nil
	}
	if !w.sent {
		w.sent = true
	}
	return w.ResponseWriter.Write(b)
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func (w *envelopeWriter) finish() {
	if !w.capturing || w.sent {
		return
	}
	w.sent = true
	msg := strings.TrimSpace(w.buf.String())
	if len(msg) > 300 {
		msg = msg[:300]
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Del("Content-Length")
	w.ResponseWriter.WriteHeader(w.status)
	_ = json.NewEncoder(w.ResponseWriter).Encode(errorEnvelope{Error: errorBody{Code: StableErrorCode(w.status), Message: msg, RequestID: w.requestID}})
}

// SetupRequestMeta installs RequestMeta as the OUTERMOST layer (call it after SetupRateLimiting, so even a 429 carries the
// request id and honours the envelope).
func (s *Server) SetupRequestMeta() {
	s.srv.Handler = RequestMeta(s.srv.Handler)
}
