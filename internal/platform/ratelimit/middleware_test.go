package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

func TestMiddleware_WithTenantContext(t *testing.T) {
	limiter := NewLimiter()
	limiter.SetQuota(QuotaTypeTenant, 2, time.Minute)
	limiter.SetQuota(QuotaTypeUser, 2, time.Minute)

	middleware := Middleware(limiter)

	tenantID := uuid.New()
	actorID := uuid.New()
	tenantCtx := &domain.TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
	}

	// Handler simples que responde OK
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrappedHandler := middleware(nextHandler)

	// Request 1 — deve passar
	req1 := httptest.NewRequest("GET", "/test", nil)
	ctx1 := context.WithValue(req1.Context(), "tenant_context", tenantCtx)
	req1 = req1.WithContext(ctx1)

	w1 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Errorf("request 1: expected 200, got %d", w1.Code)
	}

	remaining1 := w1.Header().Get("X-RateLimit-Remaining")
	if remaining1 == "" {
		t.Errorf("request 1: X-RateLimit-Remaining header missing")
	}

	// Request 2 — deve passar
	req2 := httptest.NewRequest("GET", "/test", nil)
	ctx2 := context.WithValue(req2.Context(), "tenant_context", tenantCtx)
	req2 = req2.WithContext(ctx2)

	w2 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("request 2: expected 200, got %d", w2.Code)
	}

	// Request 3 — deve ser bloqueado (tenant limit exceeded)
	req3 := httptest.NewRequest("GET", "/test", nil)
	ctx3 := context.WithValue(req3.Context(), "tenant_context", tenantCtx)
	req3 = req3.WithContext(ctx3)

	w3 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w3, req3)

	if w3.Code != http.StatusTooManyRequests {
		t.Errorf("request 3: expected 429, got %d", w3.Code)
	}

	retryAfter := w3.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Errorf("request 3: Retry-After header missing")
	}
}

func TestMiddleware_WithoutTenantContext(t *testing.T) {
	limiter := NewLimiter()
	limiter.SetQuota(QuotaTypeGlobal, 2, time.Minute)

	middleware := Middleware(limiter)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrappedHandler := middleware(nextHandler)

	// Request 1 — deve passar (global limit)
	req1 := httptest.NewRequest("GET", "/test", nil)
	w1 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Errorf("request 1: expected 200, got %d", w1.Code)
	}

	// Request 2 — deve passar
	req2 := httptest.NewRequest("GET", "/test", nil)
	w2 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("request 2: expected 200, got %d", w2.Code)
	}

	// Request 3 — deve ser bloqueado
	req3 := httptest.NewRequest("GET", "/test", nil)
	w3 := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w3, req3)

	if w3.Code != http.StatusTooManyRequests {
		t.Errorf("request 3: expected 429, got %d", w3.Code)
	}
}

func TestMiddleware_RateLimitHeaders(t *testing.T) {
	limiter := NewLimiter()
	limiter.SetQuota(QuotaTypeUser, 5, time.Minute)

	middleware := Middleware(limiter)

	tenantID := uuid.New()
	actorID := uuid.New()
	tenantCtx := &domain.TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
	}

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrappedHandler := middleware(nextHandler)

	req := httptest.NewRequest("GET", "/test", nil)
	ctx := context.WithValue(req.Context(), "tenant_context", tenantCtx)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(w, req)

	// Check headers
	remaining := w.Header().Get("X-RateLimit-Remaining")
	if remaining == "" {
		t.Errorf("X-RateLimit-Remaining header missing")
	}

	remainingInt, _ := strconv.Atoi(remaining)
	if remainingInt != 4 { // 5 - 1 = 4
		t.Errorf("expected remaining=4, got %d", remainingInt)
	}

	reset := w.Header().Get("X-RateLimit-Reset")
	if reset == "" {
		t.Errorf("X-RateLimit-Reset header missing")
	}

	resetInt, _ := strconv.ParseInt(reset, 10, 64)
	resetTime := time.Unix(resetInt, 0)
	if resetTime.Before(time.Now()) {
		t.Errorf("reset time should be in the future")
	}
}
