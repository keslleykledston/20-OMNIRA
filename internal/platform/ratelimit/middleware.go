package ratelimit

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// This middleware runs BEFORE authentication, so in production no tenant context exists yet: everything lands in the coarse global bucket
// (a process-wide safety net). The per-tenant and per-user quotas are enforced AFTER the membership is verified, by EnforceTenantUser,
// which the tenant authorization middleware calls (R-2). Counting by a tenant id taken from the URL before authorization would let a
// stranger burn another tenant's quota, so it is never done that way.

type enforcerKey struct{}

// WithEnforcer makes the limiter reachable by the post-authorization hook through the request context.
func WithEnforcer(ctx context.Context, l *Limiter) context.Context {
	return context.WithValue(ctx, enforcerKey{}, l)
}

// EnforceTenantUser applies the tenant and user quotas to an already authorized request. It writes the 429 itself and returns false
// when a quota is exceeded. Without a limiter in the context (tests, tools) it allows everything.
func EnforceTenantUser(w http.ResponseWriter, r *http.Request, tenantID, actorID uuid.UUID) bool {
	l, _ := r.Context().Value(enforcerKey{}).(*Limiter)
	if l == nil {
		return true
	}
	tenantAllowed, tenantRemaining, tenantReset := l.Allow(r.Context(), QuotaTypeTenant, tenantID.String())
	if !tenantAllowed {
		writeRateLimitExceeded(w, tenantRemaining, tenantReset)
		return false
	}
	userAllowed, userRemaining, userReset := l.Allow(r.Context(), QuotaTypeUser, actorID.String())
	if !userAllowed {
		writeRateLimitExceeded(w, userRemaining, userReset)
		return false
	}
	return true
}

// Middleware — HTTP middleware para rate limiting
func Middleware(limiter *Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extrair tenant context
			tenantCtx, ok := r.Context().Value("tenant_context").(*domain.TenantContext)
			if !ok || tenantCtx == nil {
				// Sem tenant context, aplicar rate limit global
				allowed, remaining, resetTime := limiter.Allow(r.Context(), QuotaTypeGlobal, "anonymous")
				if !allowed {
					writeRateLimitExceeded(w, remaining, resetTime)
					return
				}
				writeRateLimitHeaders(w, remaining, resetTime)
				next.ServeHTTP(w, r.WithContext(WithEnforcer(r.Context(), limiter)))
				return
			}

			// Aplicar rate limits: tenant + user
			// 1. Check tenant limit
			tenantAllowed, tenantRemaining, tenantReset := limiter.Allow(r.Context(), QuotaTypeTenant, tenantCtx.TenantID.String())
			if !tenantAllowed {
				writeRateLimitExceeded(w, tenantRemaining, tenantReset)
				return
			}

			// 2. Check user limit
			userAllowed, userRemaining, userReset := limiter.Allow(r.Context(), QuotaTypeUser, tenantCtx.ActorID.String())
			if !userAllowed {
				writeRateLimitExceeded(w, userRemaining, userReset)
				return
			}

			// Use the more restrictive remaining count
			remaining := tenantRemaining
			if userRemaining < remaining {
				remaining = userRemaining
			}

			// Use the earliest reset time
			resetTime := tenantReset
			if userReset.Before(resetTime) {
				resetTime = userReset
			}

			writeRateLimitHeaders(w, remaining, resetTime)

			next.ServeHTTP(w, r)
		})
	}
}

// writeRateLimitHeaders — escreve headers de rate limit na resposta
func writeRateLimitHeaders(w http.ResponseWriter, remaining int, resetTime time.Time) {
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	if !resetTime.IsZero() {
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))
	}
}

// writeRateLimitExceeded — escreve resposta 429 quando limite é excedido
func writeRateLimitExceeded(w http.ResponseWriter, remaining int, resetTime time.Time) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-RateLimit-Remaining", "0")
	if !resetTime.IsZero() {
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))
		w.Header().Set("Retry-After", strconv.FormatInt(resetTime.Unix()-time.Now().Unix(), 10))
	}
	w.WriteHeader(http.StatusTooManyRequests)
	fmt.Fprintf(w, `{"error":"Rate limit exceeded","retry_after":%d}`, resetTime.Unix()-time.Now().Unix())
}
