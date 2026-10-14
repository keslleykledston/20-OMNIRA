package application

import (
	"sync"
	"time"
)

// Limiter is a token bucket per key (an installation id, or a remote address for failed authentication). In-process: with several API
// replicas each enforces its own bucket, so the effective ceiling is rate x replicas; that is documented, and the limit is a protection
// against a misbehaving client, not a billing meter.
type Limiter struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]*bucket
	max     int
}

type bucket struct {
	tokens float64
	at     time.Time
}

func NewLimiter() *Limiter { return &Limiter{now: time.Now, buckets: map[string]*bucket{}, max: 10000} }

// Allow takes one token from the bucket of key, refilled at perMinute/60 per second with a burst of max(perMinute/6, 5) (ten seconds' worth).
// When it refuses it says how long until a token exists.
func (l *Limiter) Allow(key string, perMinute int) (bool, time.Duration) {
	if perMinute <= 0 {
		return false, time.Minute
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	burst := float64(perMinute) / 6
	if burst < 5 {
		burst = 5
	}
	rate := float64(perMinute) / 60 // tokens per second
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= l.max { // bounded memory: forget the oldest-looking half instead of growing forever
			l.evict(now)
		}
		b = &bucket{tokens: burst, at: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.at).Seconds() * rate
	if b.tokens > burst {
		b.tokens = burst
	}
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / rate * float64(time.Second))
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

func (l *Limiter) evict(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.at) > time.Minute {
			delete(l.buckets, k)
		}
	}
	if len(l.buckets) >= l.max {
		for k := range l.buckets {
			delete(l.buckets, k)
			if len(l.buckets) < l.max/2 {
				break
			}
		}
	}
}
