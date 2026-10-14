package adapters

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/omnira/omnira/internal/external/application"
	"github.com/omnira/omnira/internal/external/domain"
)

// Middleware authenticates /api/v1/external/* calls. The tenant of a call is the tenant of the installation behind the credential; the
// request is not consulted for it, and a request that tries to name one (query, header) is refused rather than ignored, so a client that
// believes it is choosing a tenant finds out at once.
type Middleware struct {
	auth  *application.Authenticator
	touch interface {
		Touch(ctx context.Context, p *domain.Principal)
	}
	perApp  *application.Limiter
	authBad *application.Limiter
	// FailedAuthPerMinute bounds how fast one remote address may keep presenting bad tokens (the answer turns from 401 into 429). Calls with a
	// valid token are never affected by it.
	FailedAuthPerMinute int
}

func NewMiddleware(lookup *PostgresLookup) *Middleware {
	return &Middleware{auth: application.NewAuthenticator(lookup), touch: lookup, perApp: application.NewLimiter(), authBad: application.NewLimiter(), FailedAuthPerMinute: 30}
}

// NewMiddlewareFor builds the middleware over any lookup (tests).
func NewMiddlewareFor(l application.CredentialLookup) *Middleware {
	return &Middleware{auth: application.NewAuthenticator(l), perApp: application.NewLimiter(), authBad: application.NewLimiter(), FailedAuthPerMinute: 30}
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 8 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}
	return strings.TrimSpace(h[7:]), true
}

// Authenticated wraps a handler: request id, refusal of tenant hints, bearer authentication, per-installation rate limit.
func (m *Middleware) Authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, _ = withRequestID(r)
		if r.URL.Query().Has("tenant_id") || r.Header.Get("X-Tenant-ID") != "" || r.Header.Get("X-Omnira-Acting-As") != "" {
			externalMetric.Inc("bad_request")
			WriteProblem(w, r, http.StatusBadRequest, "tenant_not_accepted", "The tenant cannot be chosen",
				"The tenant is the one of the installation behind the credential; remove tenant_id, X-Tenant-ID and X-Omnira-Acting-As.")
			return
		}
		token, ok := bearer(r)
		var p *domain.Principal
		var err error
		if ok {
			p, err = m.auth.Authenticate(r.Context(), token)
		} else {
			err = application.ErrUnauthenticated
		}
		switch {
		case err == nil:
		case errors.Is(err, application.ErrUnauthenticated):
			if m.FailedAuthPerMinute > 0 {
				if allowed, wait := m.authBad.Allow("bad:"+remoteHost(r), m.FailedAuthPerMinute); !allowed {
					externalMetric.Inc("auth_throttled")
					WriteRateLimited(w, r, wait)
					return
				}
			}
			externalMetric.Inc("unauthenticated")
			w.Header().Set("WWW-Authenticate", `Bearer realm="omnira-external"`)
			WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required", "Send Authorization: Bearer <integration token>.")
			return
		default:
			externalMetric.Inc("unavailable")
			w.Header().Set("Retry-After", "5")
			WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "Temporarily unavailable", "Try again shortly.")
			return
		}
		if allowed, wait := m.perApp.Allow("app:"+p.InstallationID.String(), p.RatePerMin); !allowed {
			externalMetric.Inc("rate_limited")
			WriteRateLimited(w, r, wait)
			return
		}
		if m.touch != nil {
			m.touch.Touch(r.Context(), p)
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// RequireScope refuses (403) a caller whose installation lacks the scope. It must sit inside Authenticated.
func RequireScope(scope domain.Scope, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required", "")
			return
		}
		if !p.Has(scope) {
			externalMetric.Inc("forbidden_scope")
			WriteProblem(w, r, http.StatusForbidden, "forbidden_scope", "Scope not granted", "This installation does not hold the scope "+string(scope)+".")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ErrInstallationInactive: the installation or credential stopped being usable between authentication and the commit of a write.
var ErrInstallationInactive = errors.New("installation or credential no longer active")
