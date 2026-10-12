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
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/opsmetrics"
	"github.com/omnira/omnira/internal/platform/ratelimit"
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
	MaxCompanies    int           // companies followed by one stream (default 100); the rest keep the list's own poll
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
	if opts.MaxCompanies <= 0 {
		opts.MaxCompanies = 100
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

// reach is one company the person serves through THIS Hub, as the database says right now, with the queues THIS Hub's contract allows.
type reach struct {
	tenant       uuid.UUID
	unrestricted bool     // the contract names no queue list: every event of the company may be signalled
	queues       []string // otherwise, the queues the contract lists (canonical text)
}

// reachOf asks, in a short transaction under the person's own session, whether they are a live member of the Hub and which companies they serve through it.
func (h *HTTPHandler) reachOf(ctx context.Context, actor, hubID uuid.UUID) ([]reach, error) {
	var out []reach
	err := platformdb.WithTenantSession(ctx, h.pool, actor, false, func(c context.Context) error {
		if err := h.authz.AuthorizeHubMember(c, actor, hubID); err != nil {
			return err
		}
		rows, err := platformdb.QuerierFromContext(c, h.pool).Query(c, `SELECT tenant_id, unrestricted, queue_texts FROM hub_reach() WHERE hub_id = $1 ORDER BY tenant_id`, hubID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r reach
			if err := rows.Scan(&r.tenant, &r.unrestricted, &r.queues); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// queueOf reads the queue of a conversation the person can read at all (their own session, RLS applies). found=false: not readable or not there. A conversation
// with no queue answers found=true and "" (it is in no queue a contract can list).
func (h *HTTPHandler) queueOf(ctx context.Context, actor, tenant, conversation uuid.UUID) (queue string, found bool, err error) {
	err = platformdb.WithTenantSession(ctx, h.pool, actor, false, func(c context.Context) error {
		var q *string
		e := platformdb.QuerierFromContext(c, h.pool).QueryRow(c, `SELECT queue_id::text FROM conversations WHERE tenant_id = $1 AND id = $2`, tenant, conversation).Scan(&q)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		found = true
		if q != nil {
			queue = *q
		}
		return nil
	})
	return queue, found, err
}

func sameKeys(a, b map[string]bool) bool {
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return len(a) == len(b)
}

// company is what the stream knows about one served company.
type company struct {
	unrestricted bool
	queues       map[string]bool
}

type queueAnswer struct {
	queue string
	found bool
	at    time.Time
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
	if !ratelimit.EnforceUser(w, r, actor) {
		return
	}
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

	var watchdog sync.WaitGroup
	defer watchdog.Wait() // runs AFTER stop(): the watchdog never touches the response once the handler has returned
	ctx, stop := context.WithCancel(r.Context())
	defer stop()
	rc := http.NewResponseController(w)
	var closing atomic.Bool // set the moment the watchdog ends the stream: nothing is written after it
	write := func(b []byte) bool {
		if closing.Load() {
			return false
		}
		_ = rc.SetWriteDeadline(time.Now().Add(ev.opts.WriteTimeout))
		if closing.Load() { // the watchdog closed the stream between the check and the arming: its "now" deadline must win
			_ = rc.SetWriteDeadline(time.Now())
			return false
		}
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	// One subscription per company (bounded); callbacks only note that something arrived (a full channel already means "pending").
	type arrival struct{ tenant, conversation uuid.UUID }
	arrivals := make(chan arrival, 128)
	var mu sync.Mutex // guards companies, subs and cache
	companies := map[uuid.UUID]company{}
	subs := map[uuid.UUID]*nats.Subscription{}
	cache := map[[2]uuid.UUID]queueAnswer{}
	apply := func(now []reach) bool {
		mu.Lock()
		defer mu.Unlock()
		if len(now) > ev.opts.MaxCompanies {
			log.Printf("hub events: %d companies for one person, following the first %d (the list keeps its own poll for the rest)", len(now), ev.opts.MaxCompanies)
			now = now[:ev.opts.MaxCompanies]
		}
		want := map[uuid.UUID]bool{}
		for _, c := range now {
			want[c.tenant] = true
			q := map[string]bool{}
			for _, id := range c.queues {
				q[id] = true
			}
			if prev, had := companies[c.tenant]; had && (prev.unrestricted != c.unrestricted || len(prev.queues) != len(q) || !sameKeys(prev.queues, q)) {
				for key := range cache { // what was learned under the old list is forgotten
					if key[0] == c.tenant {
						delete(cache, key)
					}
				}
			}
			companies[c.tenant] = company{unrestricted: c.unrestricted, queues: q}
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
			subs[c.tenant] = sub
		}
		for tenant, sub := range subs {
			if !want[tenant] {
				_ = sub.Unsubscribe()
				delete(subs, tenant)
				delete(companies, tenant)
			}
		}
		// what was learned about a conversation of a company that left (or of one whose queue list changed) is forgotten with it
		for key := range cache {
			if _, ok := companies[key[0]]; !ok {
				delete(cache, key)
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

	// The relationship is asked again APART from the event loop, so a slow write or a backlog of events cannot postpone a revocation.
	watchdog.Add(1)
	go func() {
		defer watchdog.Done()
		every := ev.opts.Recheck - ev.opts.DatabaseTimeout
		if every <= 0 {
			every = ev.opts.Recheck / 2
		}
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				c, cancel := context.WithTimeout(ctx, ev.opts.DatabaseTimeout)
				alive := true
				if ev.opts.SessionRecheck != nil {
					if err := ev.opts.SessionRecheck(c, principal); err != nil && !errors.Is(err, context.Canceled) {
						alive = false
					}
				}
				if alive {
					now, err := h.reachOf(c, actor, hubID)
					alive = err == nil && apply(now) // fail closed: a membership that cannot be confirmed ends the stream
				}
				cancel()
				if !alive && ctx.Err() == nil {
					eventsOpened.Inc("ended_access_lost")
					closing.Store(true)
					stop()
					_ = rc.SetWriteDeadline(time.Now()) // a write blocked on a client that is not reading fails now
					return
				}
			}
		}
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if !write([]byte("retry: 5000\n: connected\n\n")) {
		return
	}
	eventsOpened.Inc("opened")

	// May this arrival be signalled to THIS person through THIS Hub? The company must still be served, and when the contract lists queues the conversation's
	// queue must be one of them. The queue is read under the person's session, but the VERDICT is taken against this Hub's list, so a direct membership or
	// another Hub with a wider scope cannot widen it.
	visible := func(a arrival) bool {
		mu.Lock()
		c, ok := companies[a.tenant]
		e, hit := cache[[2]uuid.UUID{a.tenant, a.conversation}]
		mu.Unlock()
		if !ok {
			return false // a company that left, or an event that was already queued when it left
		}
		if c.unrestricted {
			return true
		}
		if !hit || time.Since(e.at) >= ev.opts.VisibilityTTL {
			qc, cancel := context.WithTimeout(ctx, ev.opts.DatabaseTimeout)
			queue, found, err := h.queueOf(qc, actor, a.tenant, a.conversation)
			cancel()
			if err != nil {
				return false // fail closed: no signal
			}
			e = queueAnswer{queue: queue, found: found, at: time.Now()}
			mu.Lock()
			if _, still := companies[a.tenant]; still {
				if len(cache) > 4096 {
					cache = map[[2]uuid.UUID]queueAnswer{}
				}
				cache[[2]uuid.UUID{a.tenant, a.conversation}] = e
			}
			c, ok = companies[a.tenant] // the scope may have changed while the query ran
			mu.Unlock()
			if !ok {
				return false
			}
		}
		return e.found && c.queues[e.queue]
	}

	lifetime := time.NewTimer(ev.opts.MaxLifetime)
	defer lifetime.Stop()
	keepalive := time.NewTicker(ev.opts.Keepalive)
	defer keepalive.Stop()
	var pending bool
	var lastSignal time.Time
	var flush <-chan time.Time // armed when a signal is owed but the minimum interval has not passed
	signal := func() bool {
		pending = false
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
			pending = true
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
			if pending && !signal() {
				return
			}
		case <-keepalive.C:
			if !write([]byte(": keepalive\n\n")) {
				return
			}
		}
	}
}
