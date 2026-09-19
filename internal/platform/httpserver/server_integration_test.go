package httpserver

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

func TestRateLimitingIntegration(t *testing.T) {
	// Create server with rate limiting
	srv := New(":8080")
	srv.SetupRateLimiting()

	// Register a simple test handler
	srv.mux.HandleFunc("GET /test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Test 1: Request without tenant context (global limit)
	req1 := httptest.NewRequest("GET", "/test", nil)
	w1 := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Errorf("request 1: expected 200, got %d", w1.Code)
	}

	remaining1 := w1.Header().Get("X-RateLimit-Remaining")
	if remaining1 == "" {
		t.Errorf("request 1: X-RateLimit-Remaining header missing")
	}

	// Test 2: Request with tenant context
	tenantID := uuid.New()
	actorID := uuid.New()
	tenantCtx := &domain.TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
	}

	req2 := httptest.NewRequest("GET", "/test", nil)
	ctx := context.WithValue(req2.Context(), "tenant_context", tenantCtx)
	req2 = req2.WithContext(ctx)

	w2 := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("request 2: expected 200, got %d", w2.Code)
	}

	remaining2 := w2.Header().Get("X-RateLimit-Remaining")
	if remaining2 == "" {
		t.Errorf("request 2: X-RateLimit-Remaining header missing")
	}

	// Verify remaining count decreased
	rem1, _ := strconv.Atoi(remaining1)
	rem2, _ := strconv.Atoi(remaining2)
	if rem2 >= rem1 {
		t.Errorf("remaining count should decrease: %d >= %d", rem2, rem1)
	}

	// Test 3: Verify reset time is in the future
	resetStr := w2.Header().Get("X-RateLimit-Reset")
	if resetStr == "" {
		t.Errorf("X-RateLimit-Reset header missing")
	}

	resetInt, _ := strconv.ParseInt(resetStr, 10, 64)
	resetTime := time.Unix(resetInt, 0)
	if resetTime.Before(time.Now()) {
		t.Errorf("reset time should be in the future")
	}
}

func TestRateLimitingBlocks(t *testing.T) {
	// Create server with very restrictive rate limit
	srv := New(":8080")
	srv.rateLimiter.SetQuota("user", 1, time.Second)
	srv.SetupRateLimiting()

	srv.mux.HandleFunc("GET /test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tenantID := uuid.New()
	actorID := uuid.New()
	tenantCtx := &domain.TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
	}

	// First request should succeed
	req1 := httptest.NewRequest("GET", "/test", nil)
	ctx1 := context.WithValue(req1.Context(), "tenant_context", tenantCtx)
	req1 = req1.WithContext(ctx1)

	w1 := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Errorf("first request: expected 200, got %d", w1.Code)
	}

	// Second request should be blocked (user limit exceeded)
	req2 := httptest.NewRequest("GET", "/test", nil)
	ctx2 := context.WithValue(req2.Context(), "tenant_context", tenantCtx)
	req2 = req2.WithContext(ctx2)

	w2 := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("second request: expected 429, got %d", w2.Code)
	}

	// Verify 429 response includes retry information
	retryAfter := w2.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Errorf("Retry-After header missing on 429")
	}

	contentType := w2.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("expected application/json, got %s", contentType)
	}
}
