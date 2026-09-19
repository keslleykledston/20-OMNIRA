package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// RealtimeEvent is what the SSE stream forwards: {type, id (conversation), timestamp, data}.
// Producers (row triggers -> Outbox -> worker publisher) put only references in data.
type RealtimeEvent struct {
	Type      string    `json:"type"` // message_received | message_status | conversation_updated
	ID        uuid.UUID `json:"id"`   // conversation id
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data"`
}

// StreamAuthorizer authorizes the user against the tenant using a short, self-contained
// transaction. It must never hold a database transaction for the stream lifetime.
type StreamAuthorizer interface {
	Authorize(ctx context.Context, userID, tenantID uuid.UUID) (*tenancydomain.TenantContext, error)
}

// RealtimeOptions tunes the stream timing; zero values use production defaults.
type RealtimeOptions struct {
	Recheck   time.Duration // membership re-authorization interval (default 30s)
	Keepalive time.Duration // heartbeat comment interval (default 20s)
}

// RealtimeHandler serves SSE from NATS. Authorization happens once (StreamMiddleware) and is
// re-checked every Recheck; a revoked membership closes the stream. No DB transaction or
// connection is held while streaming.
type RealtimeHandler struct {
	nc   *nats.Conn
	auth StreamAuthorizer
	opts RealtimeOptions
}

func NewRealtimeHandler(nc *nats.Conn, auth StreamAuthorizer, opts RealtimeOptions) *RealtimeHandler {
	if opts.Recheck <= 0 {
		opts.Recheck = 30 * time.Second
	}
	if opts.Keepalive <= 0 {
		opts.Keepalive = 20 * time.Second
	}
	return &RealtimeHandler{nc: nc, auth: auth, opts: opts}
}

// StreamMiddleware authenticates (Principal from authn) and authorizes the {tenant_id} path
// value once, injects the TenantContext and calls next WITHOUT any open transaction.
// Unknown/forbidden tenants answer 404 like the rest of the API (no enumeration oracle).
func StreamMiddleware(auth StreamAuthorizer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
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
			tc, err := auth.Authorize(r.Context(), principal.UserID, tenantID)
			if err != nil {
				http.Error(w, "tenant not found", http.StatusNotFound)
				return
			}
			next.ServeHTTP(w, r.WithContext(tenancydomain.WithTenantContext(r.Context(), tc)))
		})
	}
}

// StreamConversationEvents streams events of one conversation of the authorized tenant.
func (h *RealtimeHandler) StreamConversationEvents(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil {
		http.Error(w, "tenant context required", http.StatusUnauthorized)
		return
	}
	conversationID, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return
	}
	h.stream(w, r, tc, fmt.Sprintf("inbox.events.%s.%s", tc.TenantID, conversationID))
}

// StreamInboxEvents streams every event of the authorized tenant.
func (h *RealtimeHandler) StreamInboxEvents(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil {
		http.Error(w, "tenant context required", http.StatusUnauthorized)
		return
	}
	h.stream(w, r, tc, fmt.Sprintf("inbox.events.%s.>", tc.TenantID))
}

func (h *RealtimeHandler) stream(w http.ResponseWriter, r *http.Request, tc *tenancydomain.TenantContext, subject string) {
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
	// The server-wide WriteTimeout would cut a long-lived stream.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	msgs := make(chan *nats.Msg, 64)
	sub, err := h.nc.ChanSubscribe(subject, msgs)
	if err != nil {
		http.Error(w, "failed to subscribe", http.StatusInternalServerError)
		return
	}
	defer func() { _ = sub.Unsubscribe() }()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: do not buffer the stream
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	recheck := time.NewTicker(h.opts.Recheck)
	defer recheck.Stop()
	keepalive := time.NewTicker(h.opts.Keepalive)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-msgs:
			var event RealtimeEvent
			if err := json.Unmarshal(msg.Data, &event); err != nil {
				log.Printf("inbox realtime: dropping malformed event on %s", msg.Subject)
				continue
			}
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-recheck.C:
			// Re-authorize in a short transaction; revoked membership / inactive tenant ends the stream.
			if _, err := h.auth.Authorize(r.Context(), principal.UserID, tc.TenantID); err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf("inbox realtime: closing stream, authorization lost")
				}
				return
			}
		}
	}
}
