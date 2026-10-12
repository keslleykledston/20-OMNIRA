package adapters

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/opsmetrics"
)

// The Hub's change signal: GET /api/v1/hubs/{hub_id}/events (Server-Sent Events).
//
// The aggregated "Todas" tab reads many instances at once and cannot reuse the per-instance streams (they authorize one instance). This stream tells the
// tab "something changed in an instance you serve" so it re-reads the list through the normal REST API, which decides what the person may see. It therefore
// carries NO content at all: every event is `event: changed` with an empty object, no id of a company, conversation or person. The worst a stale
// subscription can leak is that something happened, and the stream is re-authorized every few seconds anyway.
//
//   - admission: the person is a live member of the Hub (short transaction, none is held while streaming); the companies are the live (Hub, company) pairs
//     of their grants (hub_reach), one NATS subject per company, nothing else is subscribed;
//   - a contract limited to some queues: an event of a conversation outside the scope is not signalled (the visibility is asked of the database under
//     the person's own session, reused for a few seconds);
//   - every Recheck the credential, the membership and the set of companies are asked again: a company that left is unsubscribed, a new one is subscribed,
//     and a person who lost the Hub is cut off;
//   - bounded: signals are coalesced (at most one every MinInterval), a keepalive, a maximum lifetime (the client reconnects), a limit of streams per person
//     and per process, a deadline on every write.
type EventsOptions struct {
	Recheck         time.Duration // credential, membership and companies are asked again this often (default 15s)
	MinInterval     time.Duration // at most one signal per interval (default 2s)
	Keepalive       time.Duration // comment line so proxies keep the connection (default 20s)
	MaxLifetime     time.Duration // the stream ends after this long and the client reconnects (default 30m)
	WriteTimeout    time.Duration // deadline armed before every write (default 15s)
	DatabaseTimeout time.Duration // every lookup (default 5s)
	VisibilityTTL   time.Duration // how long "this conversation is visible to the person" is reused (default 10s)
	MaxPerUser      int           // concurrent streams per person (default 4)
	MaxTotal        int           // concurrent streams per process (default 500)
	// SessionRecheck re-validates the credential the stream was opened with; an error ends the stream. nil skips it.
	SessionRecheck func(ctx context.Context, p *authn.Principal) error
}

var (
	eventsOpened  = opsmetrics.NewCounter("omnira_hub_events_streams_total", "Hub change-signal streams by how they began or ended", "outcome", "opened", "refused_limit", "refused_access", "ended_access_lost", "ended_lifetime")
	eventsSignals = opsmetrics.NewCounter("omnira_hub_events_signals_total", "Change signals written to Hub streams", "")
)

type eventsConfig struct {
	nc   *nats.Conn
	opts EventsOptions

	mu      sync.Mutex
	perUser map[uuid.UUID]int
	total   int
}

// WithEvents mounts the change signal (needs NATS). Without it the route is not served and the tab keeps polling.
func (h *HTTPHandler) WithEvents(nc *nats.Conn, opts EventsOptions) *HTTPHandler {
	if opts.Recheck <= 0 {
		opts.Recheck = 15 * time.Second
	}
	if opts.MinInterval <= 0 {
		opts.MinInterval = 2 * time.Second
	}
	if opts.Keepalive <= 0 {
		opts.Keepalive = 20 * time.Second
	}
	if opts.MaxLifetime <= 0 {
		opts.MaxLifetime = 30 * time.Minute
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = 15 * time.Second
	}
	if opts.DatabaseTimeout <= 0 {
		opts.DatabaseTimeout = 5 * time.Second
	}
	if opts.VisibilityTTL <= 0 {
		opts.VisibilityTTL = 10 * time.Second
	}
	if opts.MaxPerUser <= 0 {
		opts.MaxPerUser = 4
	}
	if opts.MaxTotal <= 0 {
		opts.MaxTotal = 500
	}
	h.events = &eventsConfig{nc: nc, opts: opts, perUser: map[uuid.UUID]int{}}
	return h
}

func (e *eventsConfig) acquire(user uuid.UUID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.total >= e.opts.MaxTotal || e.perUser[user] >= e.opts.MaxPerUser {
		return false
	}
	e.total++
	e.perUser[user]++
	return true
}

func (e *eventsConfig) release(user uuid.UUID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.total--
	if e.perUser[user]--; e.perUser[user] <= 0 {
		delete(e.perUser, user)
	}
}

// reach is one company the person serves through the Hub, as the database says right now.
type reach struct {
	tenant       uuid.UUID
	unrestricted bool // the contract names no queue list: every event of the company may be signalled
}

// reachOf asks, in a short transaction under the person's own session, whether they are a live member of the Hub and which companies they serve.
func (h *HTTPHandler) reachOf(ctx context.Context, actor, hubID uuid.UUID) ([]reach, error) {
	var out []reach
	err := platformdb.WithTenantSession(ctx, h.pool, actor, false, func(c context.Context) error {
		if err := h.authz.AuthorizeHubMember(c, actor, hubID); err != nil {
			return err
		}
		rows, err := platformdb.QuerierFromContext(c, h.pool).Query(c, `SELECT tenant_id, unrestricted FROM hub_reach() WHERE hub_id = $1`, hubID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r reach
			if err := rows.Scan(&r.tenant, &r.unrestricted); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// visibleTo asks whether the person may read the conversation (the Hub's own path of the conversations policy, queue scope included).
func (h *HTTPHandler) visibleTo(ctx context.Context, actor, tenant, conversation uuid.UUID) (bool, error) {
	var ok bool
	err := platformdb.WithTenantSession(ctx, h.pool, actor, false, func(c context.Context) error {
		return platformdb.QuerierFromContext(c, h.pool).QueryRow(c, `SELECT EXISTS (SELECT 1 FROM conversations WHERE tenant_id = $1 AND id = $2)`, tenant, conversation).Scan(&ok)
	})
	return ok, err
}

// StreamEvents serves the change signal. Mount it behind the authentication middleware ONLY (never behind UserSessionMiddleware: that holds a database
// transaction for the whole request, and a stream lasts minutes).
func (h *HTTPHandler) StreamEvents(w http.ResponseWriter, r *http.Request) {
	ev := h.events
	if ev == nil || ev.nc == nil {
		httpError(w, "not found", http.StatusNotFound)
		return
	}
	principal, err := authn.FromContext(r.Context())
	if err != nil || principal.UserID == uuid.Nil {
		httpError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	hubID, err := uuid.Parse(r.PathValue("hub_id"))
	if err != nil || r.URL.Query().Has("tenant_id") {
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}
	actor := principal.UserID
	if _, ok := w.(http.Flusher); !ok {
		httpError(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	adm, cancelAdm := context.WithTimeout(r.Context(), ev.opts.DatabaseTimeout*2)
	current, err := h.reachOf(adm, actor, hubID)
	cancelAdm()
	if err != nil {
		eventsOpened.Inc("refused_access")
		writeAccessError(w, err) // one answer for no Hub, not a member, no grant: 404
		return
	}
	if !ev.acquire(actor) {
		eventsOpened.Inc("refused_limit")
		w.Header().Set("Retry-After", "5")
		httpError(w, "too many realtime connections", http.StatusTooManyRequests)
		return
	}
	defer ev.release(actor)

	ctx, stop := context.WithCancel(r.Context())
	defer stop()
	rc := http.NewResponseController(w)
	write := func(b []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(ev.opts.WriteTimeout))
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	// One subscription per company; callbacks only note that something arrived (a full channel already means "pending").
	type arrival struct {
		tenant, conversation uuid.UUID
	}
	arrivals := make(chan arrival, 128)
	subs := map[uuid.UUID]*nats.Subscription{}
	unrestricted := map[uuid.UUID]bool{}
	var mu sync.Mutex // guards subs and unrestricted
	apply := func(now []reach) bool {
		mu.Lock()
		defer mu.Unlock()
		want := map[uuid.UUID]bool{}
		for _, c := range now {
			want[c.tenant] = true
			unrestricted[c.tenant] = c.unrestricted
			if subs[c.tenant] != nil {
				continue
			}
			tenant := c.tenant
			sub, err := ev.nc.Subscribe("inbox.events."+tenant.String()+".*", func(m *nats.Msg) {
				// subject = inbox.events.<tenant>.<conversation>: the pair was verified by the realtime bridge before it was published
				parts := strings.Split(m.Subject, ".")
				if len(parts) != 4 {
					return
				}
				conv, err := uuid.Parse(parts[3])
				if err != nil {
					return
				}
				select {
				case arrivals <- arrival{tenant: tenant, conversation: conv}:
				default: // the loop is behind: it will signal once for the whole burst
				}
			})
			if err != nil {
				log.Printf("hub events: subscribe failed: %v", err)
				return false
			}
			subs[tenant] = sub
		}
		for tenant, sub := range subs {
			if !want[tenant] {
				_ = sub.Unsubscribe()
				delete(subs, tenant)
				delete(unrestricted, tenant)
			}
		}
		return true
	}
	defer func() {
		mu.Lock()
		for _, s := range subs {
			_ = s.Unsubscribe()
		}
		mu.Unlock()
	}()
	if !apply(current) {
		httpError(w, "failed to subscribe", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if !write([]byte("retry: 5000\n: connected\n\n")) {
		return
	}
	eventsOpened.Inc("opened")

	type seen struct {
		ok bool
		at time.Time
	}
	cache := map[[2]uuid.UUID]seen{}
	visible := func(a arrival) bool {
		mu.Lock()
		open := unrestricted[a.tenant]
		mu.Unlock()
		if open {
			return true
		}
		key := [2]uuid.UUID{a.tenant, a.conversation}
		if s, hit := cache[key]; hit && time.Since(s.at) < ev.opts.VisibilityTTL {
			return s.ok
		}
		c, cancel := context.WithTimeout(ctx, ev.opts.DatabaseTimeout)
		defer cancel()
		ok, err := h.visibleTo(c, actor, a.tenant, a.conversation)
		if err != nil {
			return false // fail closed: no signal
		}
		if len(cache) > 4096 {
			cache = map[[2]uuid.UUID]seen{}
		}
		cache[key] = seen{ok: ok, at: time.Now()}
		return ok
	}

	lifetime := time.NewTimer(ev.opts.MaxLifetime)
	defer lifetime.Stop()
	recheck := time.NewTicker(ev.opts.Recheck)
	defer recheck.Stop()
	keepalive := time.NewTicker(ev.opts.Keepalive)
	defer keepalive.Stop()
	var pending atomic.Bool
	var lastSignal time.Time
	var flush <-chan time.Time // armed when a signal is owed but the minimum interval has not passed
	signal := func() bool {
		pending.Store(false)
		flush = nil
		lastSignal = time.Now()
		eventsSignals.Inc("")
		return write([]byte("event: changed\ndata: {}\n\n"))
	}
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-lifetime.C:
			eventsOpened.Inc("ended_lifetime")
			return
		case a := <-arrivals:
			if !visible(a) {
				continue
			}
			pending.Store(true)
			if wait := ev.opts.MinInterval - time.Since(lastSignal); wait > 0 {
				if flush == nil {
					flush = time.After(wait)
				}
				continue
			}
			if !signal() {
				return
			}
		case <-flush:
			if pending.Load() && !signal() {
				return
			}
		case <-keepalive.C:
			if !write([]byte(": keepalive\n\n")) {
				return
			}
		case <-recheck.C:
			if ev.opts.SessionRecheck != nil {
				if err := ev.opts.SessionRecheck(ctx, principal); err != nil && !errors.Is(err, context.Canceled) {
					eventsOpened.Inc("ended_access_lost")
					return
				}
			}
			c, cancel := context.WithTimeout(ctx, ev.opts.DatabaseTimeout)
			now, err := h.reachOf(c, actor, hubID)
			cancel()
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					eventsOpened.Inc("ended_access_lost")
				}
				return // fail closed: a membership that cannot be confirmed ends the stream
			}
			if !apply(now) {
				return
			}
		}
	}
}
