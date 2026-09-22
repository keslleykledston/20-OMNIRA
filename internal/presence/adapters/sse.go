package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/omnira/omnira/internal/platform/authn"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// SupervisorAuthorizer authorizes a user for tenant-wide presence
// visibility. StreamAuthorizer is the production implementation.
type SupervisorAuthorizer interface {
	Authorize(ctx context.Context, userID, tenantID uuid.UUID) (*tenancydomain.TenantContext, error)
}

// StreamOptions tunes SSE timing; zero values use production defaults, same
// shape as the inbox realtime stream (internal/inbox/adapters/sse.go).
type StreamOptions struct {
	Recheck     time.Duration
	Keepalive   time.Duration
	MaxLifetime time.Duration
	MaxPerUser  int
	MaxTotal    int
}

// StreamHandler serves presence transitions from NATS as SSE. Only aggregated
// transitions arrive on this subject — never heartbeats (ADR-0010 §8, §11).
// Supervisors are expected to GET the snapshot first (Handler.Snapshot), then
// open this stream for incremental updates; polling remains a fallback.
type StreamHandler struct {
	nc   *nats.Conn
	auth SupervisorAuthorizer
	opts StreamOptions

	mu      sync.Mutex
	perUser map[uuid.UUID]int
	total   int
}

func NewStreamHandler(nc *nats.Conn, auth SupervisorAuthorizer, opts StreamOptions) *StreamHandler {
	if opts.Recheck <= 0 {
		opts.Recheck = 30 * time.Second
	}
	if opts.Keepalive <= 0 {
		opts.Keepalive = 20 * time.Second
	}
	if opts.MaxLifetime <= 0 {
		opts.MaxLifetime = 30 * time.Minute
	}
	if opts.MaxPerUser <= 0 {
		opts.MaxPerUser = 5
	}
	if opts.MaxTotal <= 0 {
		opts.MaxTotal = 500
	}
	return &StreamHandler{nc: nc, auth: auth, opts: opts, perUser: map[uuid.UUID]int{}}
}

func (h *StreamHandler) acquire(user uuid.UUID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.total >= h.opts.MaxTotal || h.perUser[user] >= h.opts.MaxPerUser {
		return false
	}
	h.perUser[user]++
	h.total++
	return true
}

func (h *StreamHandler) release(user uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.perUser[user]--; h.perUser[user] <= 0 {
		delete(h.perUser, user)
	}
	h.total--
}

// Middleware authenticates and authorizes (agent.read) the {tenant_id} path
// value once, injecting the TenantContext without holding any transaction
// for the request lifetime.
func (h *StreamHandler) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := authn.FromContext(r.Context())
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		tenantID, err := uuid.Parse(r.PathValue("tenant_id"))
		if err != nil {
			http.Error(w, "invalid tenant_id", http.StatusBadRequest)
			return
		}
		tc, err := h.auth.Authorize(r.Context(), principal.UserID, tenantID)
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(tenancydomain.WithTenantContext(r.Context(), tc)))
	})
}

// Stream serves the incremental half of the supervisor flow.
func (h *StreamHandler) Stream(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil {
		http.Error(w, "tenant context required", http.StatusUnauthorized)
		return
	}
	principal, err := authn.FromContext(r.Context())
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.nc == nil {
		http.Error(w, "realtime unavailable", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	if !h.acquire(principal.UserID) {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "too many realtime connections", http.StatusTooManyRequests)
		return
	}
	defer h.release(principal.UserID)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	msgs := make(chan *nats.Msg, 64)
	sub, err := h.nc.ChanSubscribe(Subject(tc.TenantID), msgs)
	if err != nil {
		http.Error(w, "failed to subscribe", http.StatusInternalServerError)
		return
	}
	defer func() { _ = sub.Unsubscribe() }()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	lifetime := time.NewTimer(h.opts.MaxLifetime)
	defer lifetime.Stop()
	recheck := time.NewTicker(h.opts.Recheck)
	defer recheck.Stop()
	keepalive := time.NewTicker(h.opts.Keepalive)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-lifetime.C:
			return // bounded lifetime: client reconnects and re-GETs the snapshot
		case msg := <-msgs:
			var event TransitionEvent
			if err := json.Unmarshal(msg.Data, &event); err != nil {
				log.Printf("presence realtime: dropping malformed event on %s", msg.Subject)
				continue
			}
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-recheck.C:
			if _, err := h.auth.Authorize(r.Context(), principal.UserID, tc.TenantID); err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf("presence realtime: closing stream, authorization lost")
				}
				return
			}
		}
	}
}
