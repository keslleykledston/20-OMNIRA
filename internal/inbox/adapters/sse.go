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
	"sync/atomic"
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

// streamAdmissionTimeout bounds every lookup made before a stream starts (a stalled database must not hold the request).
const streamAdmissionTimeout = 10 * time.Second

// SectorScopedStreamAuthorizer is the optional third face: whether the company's sector scope (ADR-0043 S3) narrows what THIS person may be told about. When it does, the
// instance stream tells them only about conversations ConversationVisible lets them see, and a conversation stream ends when its conversation leaves their reach.
type SectorScopedStreamAuthorizer interface {
	SectorRestricted(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
}

// DelegatedStreamAuthorizer is the optional second face of a StreamAuthorizer: admission for a person who attends the instance as a Hub agent
// (`X-Omnira-Acting-As: hub:<id>`, ADR-0040 phase 05). Same rules as a delegated request, in short transactions that end before the stream starts.
type DelegatedStreamAuthorizer interface {
	// AuthorizeServing admits the person if the relationship is live and ANY of keys is held in the delegated context right now.
	AuthorizeServing(ctx context.Context, userID, tenantID, hubID uuid.UUID, keys []string) (*tenancydomain.TenantContext, error)
	// ConversationVisibleServing reports whether the conversation is visible in the delegated context (the contract's queue scope included).
	ConversationVisibleServing(ctx context.Context, userID, tenantID, hubID, conversationID uuid.UUID) (bool, error)
	// ScopeUnrestricted reports whether the contract names no queue list (every queue of the instance), so the instance stream needs no per-event filter.
	ScopeUnrestricted(ctx context.Context, userID, tenantID, hubID uuid.UUID) (bool, error)
}

// RealtimeOptions tunes the stream timing; zero values use production defaults.
type RealtimeOptions struct {
	Recheck time.Duration // membership re-authorization interval (default 30s)
	// DelegatedRecheck is the same for a Hub agent (default 15s): the longest a stream outlives a revoked grant, a lost key or a suspended instance.
	DelegatedRecheck time.Duration
	// WriteTimeout bounds every write to the client (default 15s); DatabaseTimeout bounds each lookup the stream makes (default 5s).
	WriteTimeout    time.Duration
	DatabaseTimeout time.Duration
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
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = 15 * time.Second
	}
	if opts.DatabaseTimeout <= 0 {
		opts.DatabaseTimeout = 5 * time.Second
	}
	if opts.DelegatedRecheck <= 0 {
		opts.DelegatedRecheck = 15 * time.Second
	}
	if opts.DatabaseTimeout >= opts.DelegatedRecheck { // the bound is end to end: a lookup must fit inside the recheck interval
		opts.DatabaseTimeout = opts.DelegatedRecheck / 2
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
				admit, cancel := context.WithTimeout(r.Context(), streamAdmissionTimeout)
				tc, err := da.AuthorizeServing(admit, principal.UserID, tenantID, acting.HubID, keys)
				cancel()
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
		c, cancel := context.WithTimeout(ctx, streamAdmissionTimeout)
		defer cancel()
		return da.ConversationVisibleServing(c, user, tc.TenantID, *tc.HubID, conversationID)
	}
	return h.auth.ConversationVisible(ctx, user, tc.TenantID, conversationID)
}

// seenFilter decides, per event of the instance stream, whether a Hub agent may be told about it: the event names a conversation, and a contract limited to some
// queues must not leak the existence of the others. The answer is reused for VisibilityTTL (bounded memory) and the number of lookups per second is bounded
// (a burst beyond the budget is dropped, never shown: the client polls and refetches on reconnect); members and unrestricted contracts are not filtered.
type seenFilter struct {
	ttl    time.Duration
	check  func(conversationID uuid.UUID) (bool, error)
	cache  map[uuid.UUID]seenEntry
	tokens float64
	last   time.Time
}

const (
	filterBurst      = 100.0 // lookups available at once
	filterRefillPerS = 50.0  // lookups regained per second
)

type seenEntry struct {
	ok bool
	at time.Time
}

func newSeenFilter(ttl time.Duration, check func(uuid.UUID) (bool, error)) *seenFilter {
	return &seenFilter{ttl: ttl, check: check, cache: map[uuid.UUID]seenEntry{}, tokens: filterBurst}
}

func (f *seenFilter) allow(id uuid.UUID, now time.Time) bool {
	if e, hit := f.cache[id]; hit && now.Sub(e.at) < f.ttl {
		return e.ok
	}
	if !f.last.IsZero() {
		f.tokens += now.Sub(f.last).Seconds() * filterRefillPerS
		if f.tokens > filterBurst {
			f.tokens = filterBurst
		}
	}
	f.last = now
	if f.tokens < 1 {
		return false // over budget: fail closed
	}
	f.tokens--
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
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	da, isDelegated := h.delegated(tc)
	if tc.Source == tenancydomain.AccessSourceHubServe && !isDelegated {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !h.acquire(principal.UserID) {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "too many realtime connections", http.StatusTooManyRequests)
		return
	}
	defer h.release(principal.UserID)

	// The server-wide WriteTimeout would cut a long-lived stream, so it is replaced by a deadline armed before EVERY write: a client that stops reading
	// cannot hold the goroutine (and with it the recheck) for ever, and the watchdog can force a blocked write to fail at once by moving the deadline to now.
	rc := http.NewResponseController(w)
	var closing atomic.Bool // set the moment the watchdog ends the stream: nothing is written after it
	write := func(b []byte) bool {
		if closing.Load() {
			return false
		}
		_ = rc.SetWriteDeadline(time.Now().Add(h.opts.WriteTimeout))
		// What is left after this check is irreducible concurrency, not a window: a write that has already started when the watchdog closes the stream either
		// completes (it happened before the closure) or fails on the "now" deadline; one that starts later is refused by closing.
		if closing.Load() { // the watchdog closed the stream between the check and the arming: its "now" deadline must win, not this one
			_ = rc.SetWriteDeadline(time.Now())
			return false
		}
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	msgs := make(chan *nats.Msg, 64)
	sub, err := h.nc.ChanSubscribe(subject, msgs)
	if err != nil {
		http.Error(w, "failed to subscribe", http.StatusInternalServerError)
		return
	}
	defer func() { _ = sub.Unsubscribe() }()

	// streamCtx ends when the client goes away OR when the watchdog (Hub agents) finds the access gone.
	var watchdog sync.WaitGroup
	defer watchdog.Wait() // runs AFTER stop(): the watchdog never touches the response once the handler has returned
	streamCtx, stop := context.WithCancel(r.Context())
	defer stop()

	// A member under the company's sector scope (ADR-0043 S3): the instance stream is filtered per event with the same decision the routes use. Unknown answer = restricted (fail closed).
	var sectorRestricted atomic.Bool
	var sectorFilter *seenFilter
	sectorAuth, sectorAware := h.auth.(SectorScopedStreamAuthorizer)
	if !isDelegated && sectorAware {
		first, cancelFirst := context.WithTimeout(streamCtx, h.opts.DatabaseTimeout)
		restricted, serr := sectorAuth.SectorRestricted(first, principal.UserID, tc.TenantID)
		cancelFirst()
		sectorRestricted.Store(serr != nil || restricted)
		if onlyConversation == nil {
			sectorFilter = newSeenFilter(h.opts.VisibilityTTL, func(id uuid.UUID) (bool, error) {
				c, cancel := context.WithTimeout(streamCtx, h.opts.DatabaseTimeout)
				defer cancel()
				return h.auth.ConversationVisible(c, principal.UserID, tc.TenantID, id)
			})
		}
	}

	// A Hub agent: is the contract limited to some queues? (an instance stream is filtered per event only then). Asked at admission and at every recheck.
	var unrestricted atomic.Bool
	var filter *seenFilter
	if isDelegated {
		first, cancelFirst := context.WithTimeout(streamCtx, h.opts.DatabaseTimeout)
		if open, err := da.ScopeUnrestricted(first, principal.UserID, tc.TenantID, *tc.HubID); err == nil {
			unrestricted.Store(open)
		}
		cancelFirst()
		if onlyConversation == nil { // the instance stream: tell the agent only about conversations they may see
			filter = newSeenFilter(h.opts.VisibilityTTL, func(id uuid.UUID) (bool, error) {
				c, cancel := context.WithTimeout(streamCtx, h.opts.DatabaseTimeout)
				defer cancel()
				return da.ConversationVisibleServing(c, principal.UserID, tc.TenantID, *tc.HubID, id)
			})
		}
		// The watchdog runs APART from the event loop, so a slow write or a stalled lookup in the loop cannot postpone the revocation (Codex 05).
		watchdog.Add(1)
		go func() {
			defer watchdog.Done()
			// The promise is END TO END: a check may take up to DatabaseTimeout, so the checks start that much earlier than DelegatedRecheck.
			every := h.opts.DelegatedRecheck - h.opts.DatabaseTimeout
			if every <= 0 {
				every = h.opts.DelegatedRecheck / 2
			}
			tick := time.NewTicker(every)
			defer tick.Stop()
			for {
				select {
				case <-streamCtx.Done():
					return
				case <-tick.C:
					c, cancel := context.WithTimeout(streamCtx, h.opts.DatabaseTimeout)
					alive := h.delegatedStillHolds(c, da, principal, tc, onlyConversation, &unrestricted)
					cancel()
					if !alive && streamCtx.Err() == nil {
						log.Printf("inbox realtime: closing delegated stream, authorization lost")
						closing.Store(true)
						stop()
						_ = rc.SetWriteDeadline(time.Now()) // a write blocked on a client that is not reading fails now
						return
					}
				}
			}
		}()
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: do not buffer the stream
	if !write([]byte(": connected\n\n")) {
		return
	}

	lifetime := time.NewTimer(h.opts.MaxLifetime)
	defer lifetime.Stop()
	var recheckC <-chan time.Time
	if !isDelegated { // members: rechecked in the loop, as before
		recheck := time.NewTicker(h.opts.Recheck)
		defer recheck.Stop()
		recheckC = recheck.C
	}
	keepalive := time.NewTicker(h.opts.Keepalive)
	defer keepalive.Stop()

	for {
		if streamCtx.Err() != nil { // the select below picks among ready cases at random: a stopped stream must not deliver a queued event
			return
		}
		select {
		case <-streamCtx.Done():
			return
		case <-lifetime.C:
			return // bounded lifetime: the client reconnects (and refetches) with backoff
		case msg := <-msgs:
			var event RealtimeEvent
			if err := json.Unmarshal(msg.Data, &event); err != nil {
				log.Printf("inbox realtime: dropping malformed event on %s", msg.Subject)
				continue
			}
			if filter != nil && !unrestricted.Load() && !filter.allow(event.ID, time.Now()) {
				continue
			}
			if sectorFilter != nil && sectorRestricted.Load() && !sectorFilter.allow(event.ID, time.Now()) {
				continue
			}
			if !write(sseFrame(event)) {
				return
			}
		case <-keepalive.C:
			if !write([]byte(": keepalive\n\n")) {
				return
			}
		case <-recheckC:
			// A logged-out / revoked session or device ends the stream (R-3), then membership is re-authorized in a short transaction.
			if h.opts.SessionRecheck != nil {
				if err := h.opts.SessionRecheck(r.Context(), principal); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("inbox realtime: closing stream, session revoked or expired")
					return
				}
			}
			// Re-authorize in a short transaction; revoked membership / inactive tenant ends the stream.
			if _, err := h.auth.Authorize(r.Context(), principal.UserID, tc.TenantID); err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf("inbox realtime: closing stream, authorization lost")
				}
				return
			}
			if sectorAware {
				c, cancel := context.WithTimeout(r.Context(), h.opts.DatabaseTimeout)
				restricted, serr := sectorAuth.SectorRestricted(c, principal.UserID, tc.TenantID)
				sectorRestricted.Store(serr != nil || restricted)
				// a conversation stream ends the moment its conversation leaves the person's reach (a transfer to another queue, a queue they left)
				visible := true
				if onlyConversation != nil && (serr != nil || restricted) {
					visible, serr = h.auth.ConversationVisible(c, principal.UserID, tc.TenantID, *onlyConversation)
				}
				cancel()
				if (serr != nil && !errors.Is(serr, context.Canceled)) || !visible {
					log.Printf("inbox realtime: closing stream, conversation left the person's reach")
					return
				}
			}
		}
	}
}

// delegatedStillHolds asks again, live, in short delegated transactions: the credential of the session, the relationship (grant, contract, hub, instance,
// account) with the key, and for a conversation stream whether the conversation is still inside the contract's queue scope. Any error ends the stream (fail closed).
func (h *RealtimeHandler) delegatedStillHolds(ctx context.Context, da DelegatedStreamAuthorizer, principal *authn.Principal, tc *tenancydomain.TenantContext, onlyConversation *uuid.UUID, unrestricted *atomic.Bool) bool {
	if h.opts.SessionRecheck != nil {
		if err := h.opts.SessionRecheck(ctx, principal); err != nil {
			return false
		}
	}
	if _, err := da.AuthorizeServing(ctx, principal.UserID, tc.TenantID, *tc.HubID, []string{"conversation.read"}); err != nil {
		return false
	}
	if onlyConversation != nil {
		if visible, err := da.ConversationVisibleServing(ctx, principal.UserID, tc.TenantID, *tc.HubID, *onlyConversation); err != nil || !visible {
			return false
		}
		return true
	}
	open, err := da.ScopeUnrestricted(ctx, principal.UserID, tc.TenantID, *tc.HubID)
	if err != nil {
		return false
	}
	unrestricted.Store(open)
	return true
}
