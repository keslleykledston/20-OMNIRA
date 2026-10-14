// Package adapters is the HTTP and PostgreSQL side of the external (Headless) integration, ADR-0046.
//
// Rule for every query in this package: the tenant is the tenant of the authenticated installation (Principal.TenantID, read from the
// database by the credential), it appears as an explicit predicate, and no request field can change it. scripts and tests/lint
// (tenant_predicate_test.go) fail the build when a statement here does not mention tenant_id.
package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/external/domain"
	"github.com/omnira/omnira/internal/platform/opsmetrics"
)

// externalMetric counts how external requests ended. A closed set of outcomes: never an id, a token, a tenant or a path.
var externalMetric = opsmetrics.NewCounter("omnira_external_requests_total", "Requests to /api/v1/external by outcome", "outcome",
	"served", "unauthenticated", "rate_limited", "auth_throttled", "forbidden_scope", "not_found", "bad_request", "conflict", "precondition_failed",
	"unprocessable", "unavailable", "error")

type ctxKey int

const (
	principalKey ctxKey = iota
	requestIDKey
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,64}$`)

// WithPrincipal / PrincipalFrom carry the authenticated installation through the request.
func WithPrincipal(ctx context.Context, p *domain.Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

func PrincipalFrom(ctx context.Context) (*domain.Principal, bool) {
	p, ok := ctx.Value(principalKey).(*domain.Principal)
	return p, ok && p != nil
}

func RequestIDFrom(ctx context.Context) string {
	s, _ := ctx.Value(requestIDKey).(string)
	return s
}

// withRequestID takes a well-formed X-Request-ID from the caller (so the CRM can correlate) or makes one; anything else is replaced.
func withRequestID(r *http.Request) (*http.Request, string) {
	id := r.Header.Get("X-Request-ID")
	if !requestIDPattern.MatchString(id) {
		id = uuid.NewString()
	}
	return r.WithContext(context.WithValue(r.Context(), requestIDKey, id)), id
}

// Problem is an RFC 9457 problem document. Extension members: code (stable, machine readable) and request_id.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Code      string `json:"code"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteProblem answers with application/problem+json. It never includes internals: details are fixed sentences chosen by the caller.
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, code, title, detail string) {
	h := w.Header()
	h.Set("Content-Type", "application/problem+json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if id := RequestIDFrom(r.Context()); id != "" {
		h.Set("X-Request-ID", id)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{Type: "urn:omnira:problem:" + code, Title: title, Status: status, Detail: detail, Code: code, RequestID: RequestIDFrom(r.Context())})
}

// WriteRateLimited answers 429 with Retry-After in whole seconds (rounded up, at least 1).
func WriteRateLimited(w http.ResponseWriter, r *http.Request, wait time.Duration) {
	secs := int((wait + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	WriteProblem(w, r, http.StatusTooManyRequests, "rate_limited", "Too many requests", "Slow down and retry after the indicated time.")
}

// WriteJSON writes a normal external response (never cached).
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if id := RequestIDFrom(r.Context()); id != "" {
		h.Set("X-Request-ID", id)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
