package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestAllow_WithinLimit(t *testing.T) {
	limiter := NewLimiter()
	limiter.SetQuota(QuotaTypeUser, 5, time.Minute)

	ctx := context.Background()

	// First 5 requests should be allowed
	for i := 0; i < 5; i++ {
		allowed, remaining, _ := limiter.Allow(ctx, QuotaTypeUser, "user123")
		if !allowed {
			t.Errorf("request %d should be allowed", i+1)
		}
		expectedRemaining := 5 - (i + 1)
		if remaining != expectedRemaining {
			t.Errorf("request %d: expected remaining=%d, got %d", i+1, expectedRemaining, remaining)
		}
	}
}

func TestAllow_ExceedsLimit(t *testing.T) {
	limiter := NewLimiter()
	limiter.SetQuota(QuotaTypeUser, 3, time.Minute)

	ctx := context.Background()

	// Make 3 allowed requests
	for i := 0; i < 3; i++ {
		allowed, _, _ := limiter.Allow(ctx, QuotaTypeUser, "user123")
		if !allowed {
			t.Errorf("request %d should be allowed", i+1)
		}
	}

	// 4th request should be denied
	allowed, remaining, _ := limiter.Allow(ctx, QuotaTypeUser, "user123")
	if allowed {
		t.Errorf("4th request should be denied")
	}

	if remaining != 0 {
		t.Errorf("expected remaining=0, got %d", remaining)
	}
}

func TestAllow_DifferentUsers(t *testing.T) {
	limiter := NewLimiter()
	limiter.SetQuota(QuotaTypeUser, 2, time.Minute)

	ctx := context.Background()

	// User A makes 2 requests
	limiter.Allow(ctx, QuotaTypeUser, "userA")
	limiter.Allow(ctx, QuotaTypeUser, "userA")

	// User A's 3rd should be denied
	allowed, _, _ := limiter.Allow(ctx, QuotaTypeUser, "userA")
	if allowed {
		t.Errorf("userA's 3rd request should be denied")
	}

	// User B should still have quota
	allowed, remaining, _ := limiter.Allow(ctx, QuotaTypeUser, "userB")
	if !allowed {
		t.Errorf("userB's 1st request should be allowed")
	}

	if remaining != 1 {
		t.Errorf("userB: expected remaining=1, got %d", remaining)
	}
}

func TestAllow_ExpiredTokens(t *testing.T) {
	limiter := NewLimiter()
	limiter.SetQuota(QuotaTypeUser, 2, 100*time.Millisecond)

	ctx := context.Background()

	// Make 2 requests
	limiter.Allow(ctx, QuotaTypeUser, "user123")
	limiter.Allow(ctx, QuotaTypeUser, "user123")

	// 3rd should be denied
	allowed, _, _ := limiter.Allow(ctx, QuotaTypeUser, "user123")
	if allowed {
		t.Errorf("3rd request should be denied before window expires")
	}

	// Wait for window to expire
	time.Sleep(150 * time.Millisecond)

	// Should be allowed now (tokens expired)
	allowed, _, _ = limiter.Allow(ctx, QuotaTypeUser, "user123")
	if !allowed {
		t.Errorf("request should be allowed after window expires")
	}
}

func TestGetStatus(t *testing.T) {
	limiter := NewLimiter()
	limiter.SetQuota(QuotaTypeUser, 10, time.Minute)

	ctx := context.Background()

	// Initial status
	used, limit, _ := limiter.GetStatus(QuotaTypeUser, "user123")
	if used != 0 || limit != 10 {
		t.Errorf("initial: expected used=0 limit=10, got used=%d limit=%d", used, limit)
	}

	// After 3 requests
	limiter.Allow(ctx, QuotaTypeUser, "user123")
	limiter.Allow(ctx, QuotaTypeUser, "user123")
	limiter.Allow(ctx, QuotaTypeUser, "user123")

	used, limit, _ = limiter.GetStatus(QuotaTypeUser, "user123")
	if used != 3 || limit != 10 {
		t.Errorf("after 3 requests: expected used=3 limit=10, got used=%d limit=%d", used, limit)
	}
}

func TestReset(t *testing.T) {
	limiter := NewLimiter()
	limiter.SetQuota(QuotaTypeUser, 2, time.Minute)

	ctx := context.Background()

	// Make 2 requests
	limiter.Allow(ctx, QuotaTypeUser, "user123")
	limiter.Allow(ctx, QuotaTypeUser, "user123")

	// Should be at limit
	allowed, _, _ := limiter.Allow(ctx, QuotaTypeUser, "user123")
	if allowed {
		t.Errorf("should be at limit")
	}

	// Reset
	limiter.Reset(QuotaTypeUser, "user123")

	// Should be allowed again
	allowed, _, _ = limiter.Allow(ctx, QuotaTypeUser, "user123")
	if !allowed {
		t.Errorf("should be allowed after reset")
	}
}

func TestMultipleQuotaTypes(t *testing.T) {
	limiter := NewLimiter()
	limiter.SetQuota(QuotaTypeTenant, 100, time.Minute)
	limiter.SetQuota(QuotaTypeUser, 10, time.Minute)

	ctx := context.Background()

	// Both should start with quota
	tenantAllowed, tenantRemaining, _ := limiter.Allow(ctx, QuotaTypeTenant, "tenant123")
	if !tenantAllowed || tenantRemaining != 99 {
		t.Errorf("tenant: expected allowed=true remaining=99")
	}

	userAllowed, userRemaining, _ := limiter.Allow(ctx, QuotaTypeUser, "user123")
	if !userAllowed || userRemaining != 9 {
		t.Errorf("user: expected allowed=true remaining=9")
	}
}
