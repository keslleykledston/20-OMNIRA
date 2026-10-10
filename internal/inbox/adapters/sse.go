package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// RealtimeEvent is what the SSE stream forwards: {type, id (conversation), timestamp, data}.
// Producers (row triggers -> Outbox -> worker publisher) put only references in data.
//
// EventID and Version (additive, MOBILE.READINESS) let any client de-duplicate events and recognise the payload version: the
// same event can legitimately reach a client twice (tenant stream + conversation stream, or a reconnect). Events from an older
// bridge carry neither; they are forwarded as they are. There is no replay: the stream is best-effort and a client that reconnects
// refetches through the REST API.
type RealtimeEvent struct {
	EventID   string    `json:"event_id,omitempty"` // unique per event; also sent as the SSE `id:` field
	Version   int       `json:"v,omitempty"`        // payload version, 1 today
	Type      string    `json:"type"`               // message_received | message_status | conversation_updated
	ID        uuid.UUID `json:"id"`                 // conversation id
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data"`
}

// sseFrame renders one event as an SSE frame. The data line keeps the exact JSON shape the web already consumes; the optional
// `id:` line is ignored by clients that only read `data:`.
func sseFrame(e RealtimeEvent) []byte {
	data, _ := json.Marshal(e)
	var b []byte
	if e.EventID != "" && !strings.ContainsAny(e.EventID, "\r\n") {
		b = append(b, "id: "+e.EventID+"\n"...)
	}
	b = append(b, "data: "...)
	b = append(b, data...)
	return append(b, '\n', '\n')
}

// StreamAuthorizer authorizes the user against the tenant using a short, self-contained
// transaction. It must never hold a database transaction for the stream lifetime.
type StreamAuthorizer interface {
	Authorize(ctx context.Context, userID, tenantID uuid.UUID) (*tenancydomain.TenantContext, error)
	// ConversationVisible reports whether the conversation exists in the tenant and is visible to the user
	// (short transaction, RLS applies). Used to refuse streams for unknown/foreign conversation ids.
	ConversationVisible(ctx context.Context, userID, tenantID, conversationID uuid.UUID) (bool, error)
}

// DelegatedStreamAuthorizer is the optional second face of a StreamAuthorizer: admission for a person who attends the instance as a Hub agent
// (`X-Omnira-Acting-As: hub:<id>`, ADR-0040 phase 05). Same rules as a delegated request, in short transactions that end before the stream starts.
type DelegatedStreamAuthorizer interface {
	// AuthorizeServing admits the person if the relationship is live and ANY of keys is held in the delegated context right now.
	AuthorizeServing(ctx context.Context, userID, tenantID, hubID uuid.UUID, keys []string) (*tenancydomain.TenantContext, error)
	// ConversationVisibleServing reports whether the conversation is visible in the delegated context (the contract's queue scope included).
	ConversationVisibleServing(ctx context.Context, userID, tenantID, hubID, conversationID uuid.UUID) (bool, error)
}

// RealtimeOptions tunes the stream timing; zero values use production defaults.
type RealtimeOptions struct {
	Recheck time.Duration // membership re-authorization interval (default 30s)
	// DelegatedRecheck is the same for a Hub agent (default 15s): the longest a stream outlives a revoked grant, a lost key or a suspended instance.
	DelegatedRecheck time.Duration
	// VisibilityTTL is how long the answer "this conversation is visible to the Hub agent" is reused on the instance stream (default 10s).
	VisibilityTTL time.Duration
	Keepalive     time.Duration // heartbeat comment interval (default 20s)
	MaxLifetime   time.Duration // a stream is closed after this long; clients reconnect (default 30m)
	MaxPerUser    int           // concurrent streams per user (default 10)
	MaxTotal      int           // concurrent streams per process (default 2000)
	// SessionRecheck re-validates the credential the stream was opened with (cookie session or device token); an error ends the stream.
	// nil skips it (a Bearer ID token has nothing server-side to revoke).
	SessionRecheck func(ctx context.Context, p *authn.Principal) error
}

// RealtimeHandler serves SSE from NATS. Authorization happens once (StreamMiddleware) and is
// re-checked every Recheck; a revoked membership closes the stream. No DB transaction or
// connection is held while streaming.
type RealtimeHandler struct {
	nc   *nats.Conn
	auth StreamAuthorizer
	opts RealtimeOptions

	mu      sync.Mutex
	perUser map[uuid.UUID]int
	total   int
}

func NewRealtimeHandler(nc *nats.Conn, auth StreamAuthorizer, opts RealtimeOptions) *RealtimeHandler {
	if opts.Recheck <= 0 {
		opts.Recheck = 30 * time.Second
	}
	if opts.DelegatedRecheck <= 0 {
		opts.DelegatedRecheck = 15 * time.Second
	}
	if opts.VisibilityTTL <= 0 {
		opts.VisibilityTTL = 10 * time.Second
	}
	if opts.Keepalive <= 0 {
		opts.Keepalive = 20 * time.Second
	}
	if opts.MaxLifetime <= 0 {
		opts.MaxLifetime = 30 * time.Minute
	}
	if opts.MaxPerUser <= 0 {
		opts.MaxPerUser = 10
	}
	if opts.MaxTotal <= 0 {
		opts.MaxTotal = 2000
	}
	return &RealtimeHandler{nc: nc, auth: auth, opts: opts, perUser: map[uuid.UUID]int{}}
}

// acquire reserves a stream slot for the user; false when a per-user or global ceiling is reached.
// Every stream costs a goroutine, a NATS subscription, a channel and timers, and has no write deadline.
func (h *RealtimeHandler) acquire(user uuid.UUID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.total >= h.opts.MaxTotal || h.perUser[user] >= h.opts.MaxPerUser {
		return false
	}
	h.perUser[user]++
	h.total++
	return true
}

func (h *RealtimeHandler) release(user uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.perUser[user]--; h.perUser[user] <= 0 {
		delete(h.perUser, user)
	}
	h.total--
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
			// The context the stream ACTS in. A malformed value is refused, never read as "member".
			acting, aerr := tenancydomain.ParseActingAs(r.Header.Get(tenancydomain.ActingAsHeader))
			if aerr != nil {
				http.Error(w, "invalid acting context", http.StatusBadRequest)
				return
			}
			if acting.IsHub() {
				// Only a route that declared itself delegable (and the keys it needs) opens for a Hub agent; everything else is a 404, as for a request.
				da, ok := auth.(DelegatedStreamAuthorizer)
				keys := tenancyadapters.DelegableKeys(r.Context())
				if !ok || len(keys) == 0 {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				tc, err := da.AuthorizeServing(r.Context(), principal.UserID, tenantID, acting.HubID, keys)
				if err != nil {
					if !errors.Is(err, tenancyadapters.ErrServeDenied) {
						log.Printf("inbox realtime: delegated admission: %v", err)
					}
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				next.ServeHTTP(w, r.WithContext(tenancydomain.WithTenantContext(r.Context(), tc)))
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
	if principal, perr := authn.FromContext(r.Context()); perr == nil {
		visible, verr := h.conversationVisible(r.Context(), principal.UserID, tc, conversationID)
		if verr != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if !visible { // unknown and foreign ids are indistinguishable
			http.Error(w, "conversation not found", http.StatusNotFound)
			return
		}
	}
	h.stream(w, r, tc, fmt.Sprintf("inbox.events.%s.%s", tc.TenantID, conversationID), &conversationID)
}

// StreamInboxEvents streams every event of the authorized tenant.
func (h *RealtimeHandler) StreamInboxEvents(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil {
		http.Error(w, "tenant context required", http.StatusUnauthorized)
		return
	}
	h.stream(w, r, tc, fmt.Sprintf("inbox.events.%s.>", tc.TenantID), nil)
}

// delegated reports whether the stream was admitted for a Hub agent, and the authorizer that can recheck it.
func (h *RealtimeHandler) delegated(tc *tenancydomain.TenantContext) (DelegatedStreamAuthorizer, bool) {
	if tc.Source != tenancydomain.AccessSourceHubServe || tc.HubID == nil {
		return nil, false
	}
	da, ok := h.auth.(DelegatedStreamAuthorizer)
	return da, ok
}

func (h *RealtimeHandler) conversationVisible(ctx context.Context, user uuid.UUID, tc *tenancydomain.TenantContext, conversationID uuid.UUID) (bool, error) {
	if tc.Source == tenancydomain.AccessSourceHubServe {
		da, ok := h.delegated(tc)
		if !ok {
			return false, nil
		}
		return da.ConversationVisibleServing(ctx, user, tc.TenantID, *tc.HubID, conversationID)
	}
	return h.auth.ConversationVisible(ctx, user, tc.TenantID, conversationID)
}

// seenFilter decides, per event of the instance stream, whether a Hub agent may be told about it: the event names a conversation, and a contract limited to some
// queues must not leak the existence of the others. The answer is reused for VisibilityTTL (bounded memory); members are not filtered.
type seenFilter struct {
	ttl   time.Duration
	check func(conversationID uuid.UUID) (bool, error)
	cache map[uuid.UUID]seenEntry
}

type seenEntry struct {
	ok bool
	at time.Time
}

func (f *seenFilter) allow(id uuid.UUID, now time.Time) bool {
	if e, hit := f.cache[id]; hit && now.Sub(e.at) < f.ttl {
		return e.ok
	}
	ok, err := f.check(id)
	if err != nil {
		return false // fail closed: an event is better dropped than shown to someone who may not see it (the client refetches on reconnect)
	}
	if len(f.cache) >= 1024 {
		f.cache = map[uuid.UUID]seenEntry{}
	}
	f.cache[id] = seenEntry{ok: ok, at: now}
	return ok
}

func (h *RealtimeHandler) stream(w http.ResponseWriter, r *http.Request, tc *tenancydomain.TenantContext, subject string, onlyConversation *uuid.UUID) {
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

	lifetime := time.NewTimer(h.opts.MaxLifetime)
	defer lifetime.Stop()
	da, isDelegated := h.delegated(tc)
	if tc.Source == tenancydomain.AccessSourceHubServe && !isDelegated {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	interval := h.opts.Recheck
	var filter *seenFilter
	if isDelegated {
		interval = h.opts.DelegatedRecheck
		if onlyConversation == nil { // the instance stream: tell the agent only about conversations they may see
			filter = &seenFilter{ttl: h.opts.VisibilityTTL, cache: map[uuid.UUID]seenEntry{}, check: func(id uuid.UUID) (bool, error) {
				return da.ConversationVisibleServing(r.Context(), principal.UserID, tc.TenantID, *tc.HubID, id)
			}}
		}
	}
	recheck := time.NewTicker(interval)
	defer recheck.Stop()
	keepalive := time.NewTicker(h.opts.Keepalive)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-lifetime.C:
			return // bounded lifetime: the client reconnects (and refetches) with backoff
		case msg := <-msgs:
			var event RealtimeEvent
			if err := json.Unmarshal(msg.Data, &event); err != nil {
				log.Printf("inbox realtime: dropping malformed event on %s", msg.Subject)
				continue
			}
			if filter != nil && !filter.allow(event.ID, time.Now()) {
				continue
			}
			_, _ = w.Write(sseFrame(event))
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-recheck.C:
			// A logged-out / revoked session or device ends the stream (R-3), then membership is re-authorized in a short transaction.
			if h.opts.SessionRecheck != nil {
				if err := h.opts.SessionRecheck(r.Context(), principal); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("inbox realtime: closing stream, session revoked or expired")
					return
				}
			}
			if isDelegated {
				// A Hub agent: the relationship (grant, contract, hub, instance, account) and the key are asked again, live, in a short delegated
				// transaction; a conversation stream also asks whether the conversation is still inside the contract's queue scope.
				if _, err := da.AuthorizeServing(r.Context(), principal.UserID, tc.TenantID, *tc.HubID, []string{"conversation.read"}); err != nil {
					if !errors.Is(err, context.Canceled) {
						log.Printf("inbox realtime: closing delegated stream, authorization lost")
					}
					return
				}
				if onlyConversation != nil {
					if visible, err := da.ConversationVisibleServing(r.Context(), principal.UserID, tc.TenantID, *tc.HubID, *onlyConversation); err != nil || !visible {
						return
					}
				}
				continue
			}
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
