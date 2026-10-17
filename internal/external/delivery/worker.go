package delivery

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/external/adapters"
	"github.com/omnira/omnira/internal/external/netpolicy"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/opsmetrics"
)

// deliveryMetric counts how webhook attempts end (closed vocabulary: never a URL, a tenant or an id).
var deliveryMetric = opsmetrics.NewCounter("omnira_external_webhook_attempts_total", "Webhook delivery attempts by outcome", "outcome",
	"delivered", "retry", "dead", "cancelled", "timeout", "connect", "tls", "blocked_address", "http_status", "redirect", "not_visible", "revoked", "internal")

// SecretOpener decrypts the signing secrets stored by the administration (AES-256-GCM, ADR-0009).
type SecretOpener interface {
	Decrypt(ciphertext []byte) ([]byte, error)
}

// Worker moves facts from the journal to the receivers. It is safe to run several: a claim hands each delivery to one of them (SKIP LOCKED + a lease), a
// crashed worker's claim expires by itself, and a receiver may see the same delivery twice (at least once) — it deduplicates by delivery_id / event_id.
type Worker struct {
	pool    *pgxpool.Pool
	secrets SecretOpener
	client  *http.Client
	now     func() time.Time
	jitter  func() float64
	Lease   time.Duration // how long a claimed delivery stays hidden from the other workers
	Window  time.Duration // how long a fact is retried
	Batch   int
	// Parallel is how many subscriptions are served at once (one slow receiver must not hold the others back).
	Parallel int
	// AfterClaim runs once per round, between the claim and the first send. Tests use it to change the world in that window (pause, revoke).
	AfterClaim func()
	mu         sync.Mutex
}

// errPaused: the subscription was paused after the delivery was claimed; nothing is sent and the lease hands the delivery back.
var errPaused = errors.New("delivery: the subscription is paused")

func NewWorker(pool *pgxpool.Pool, secrets SecretOpener, client *http.Client) *Worker {
	return &Worker{pool: pool, secrets: secrets, client: client, now: time.Now,
		jitter: func() float64 { return (rand.Float64()*2 - 1) * 0.2 }, Lease: 2 * time.Minute, Window: RetryWindow, Batch: 50, Parallel: 8}
}

// WithClock is for tests.
func (w *Worker) WithClock(now func() time.Time, jitter func() float64) *Worker {
	w.now, w.jitter = now, jitter
	return w
}

type claimed struct {
	deliveryID, tenantID, subscriptionID, installationID uuid.UUID
	attempts                                             int
	firstAttempt                                         *time.Time
	url, keyID, prevKeyID                                string
	secretCT, prevCT                                     []byte
	prevValidUntil                                       *time.Time
	event                                                adapters.EventDTO
	originInstallation                                   *uuid.UUID
}

// Step runs one round: number nothing (the sequencer does), fan out, claim, send. It returns how many deliveries it attempted.
func (w *Worker) Step(ctx context.Context) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.pool.Exec(ctx, `SELECT webhook_fanout(200)`); err != nil {
		return 0, fmt.Errorf("fan-out: %w", err)
	}
	rows, err := w.pool.Query(ctx, `
		SELECT delivery_id, tenant_id, subscription_id, installation_id, attempts, first_attempt_at, url, key_id, secret_ciphertext, previous_key_id, previous_secret_ciphertext,
		       previous_valid_until, event_id, event_seq, event_type, event_version, aggregate_type, aggregate_id, conversation_id, aggregate_version, origin_kind,
		       origin_installation_id, occurred_at, payload
		  FROM webhook_claim($1, make_interval(secs => $2))`, w.Batch, w.Lease.Seconds())
	if err != nil {
		return 0, fmt.Errorf("claim: %w", err)
	}
	var batch []claimed
	for rows.Next() {
		var c claimed
		var prevKey *string
		var payload map[string]any
		if err := rows.Scan(&c.deliveryID, &c.tenantID, &c.subscriptionID, &c.installationID, &c.attempts, &c.firstAttempt, &c.url, &c.keyID, &c.secretCT, &prevKey, &c.prevCT, &c.prevValidUntil,
			&c.event.EventID, &c.event.Seq, &c.event.Type, &c.event.Version, &c.event.Aggregate.Type, &c.event.Aggregate.ID, &c.event.ConversationID, &c.event.AggregateVersion, &c.event.Origin.Kind,
			&c.originInstallation, &c.event.OccurredAt, &payload); err != nil {
			rows.Close()
			return 0, err
		}
		if prevKey != nil {
			c.prevKeyID = *prevKey
		}
		c.event.TenantID = c.tenantID
		c.event.Data = payload
		c.event.Origin.Mine = c.originInstallation != nil && *c.originInstallation == c.installationID
		batch = append(batch, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if w.AfterClaim != nil && len(batch) > 0 {
		w.AfterClaim()
	}
	// Subscriptions are independent: a receiver that is slow or down delays only its own deliveries. Inside one subscription the order of the claim (by sequence) is kept.
	bySub := map[uuid.UUID][]claimed{}
	var order []uuid.UUID
	for _, c := range batch {
		if _, seen := bySub[c.subscriptionID]; !seen {
			order = append(order, c.subscriptionID)
		}
		bySub[c.subscriptionID] = append(bySub[c.subscriptionID], c)
	}
	sem := make(chan struct{}, w.Parallel)
	var wg sync.WaitGroup
	for _, id := range order {
		group := bySub[id]
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			for _, c := range group {
				w.deliver(ctx, c)
			}
		}()
	}
	wg.Wait()
	return len(batch), nil
}

// Run loops until the context ends, sleeping when there is nothing to do.
func (w *Worker) Run(ctx context.Context, idle time.Duration) {
	for {
		n, err := w.Step(ctx)
		if err != nil && ctx.Err() == nil {
			deliveryMetric.Inc("internal")
		}
		if ctx.Err() != nil {
			return
		}
		if n == 0 || err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(idle):
			}
		}
	}
}

func (w *Worker) complete(ctx context.Context, c claimed, ok bool, status int, class string, retryAt *time.Time, giveUp bool) {
	var st *int
	if status != 0 {
		st = &status
	}
	if _, err := w.pool.Exec(ctx, `SELECT webhook_complete($1,$2,$3,$4,$5,$6,$7,$8)`, c.deliveryID, c.tenantID, c.attempts, ok, st, class, retryAt, giveUp); err != nil {
		deliveryMetric.Inc("internal")
	}
}

func (w *Worker) deliver(ctx context.Context, c claimed) {
	// 1. the subscription must still be active and the fact still visible to the installation NOW (a pause or an allow-list change after the claim stops the call)
	visible, err := w.stillVisible(ctx, c)
	if errors.Is(err, errPaused) {
		return
	}
	if err != nil {
		deliveryMetric.Inc("internal") // the lease will hand it back
		return
	}
	if !visible {
		w.complete(ctx, c, false, 0, "not_visible", nil, true)
		deliveryMetric.Inc("not_visible")
		return
	}
	// 2. the exact bytes that are signed are the bytes that are sent
	body, err := json.Marshal(c.event)
	if err != nil {
		deliveryMetric.Inc("internal")
		return
	}
	secret, err := w.secrets.Decrypt(c.secretCT)
	if err != nil {
		w.complete(ctx, c, false, 0, "internal", nil, false)
		deliveryMetric.Inc("internal")
		return
	}
	var prev []byte
	if c.prevCT != nil && c.prevValidUntil != nil && c.prevValidUntil.After(w.now()) {
		prev, _ = w.secrets.Decrypt(c.prevCT)
	}
	ts := w.now().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		w.fail(ctx, c, 0, "blocked_address")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "OMNIRA-Webhook/1")
	req.Header.Set("X-Omnira-Signature", SignatureHeader(secret, prev, ts, body))
	req.Header.Set("X-Omnira-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Omnira-Event-ID", c.event.EventID.String())
	req.Header.Set("X-Omnira-Delivery-ID", c.deliveryID.String())
	req.Header.Set("X-Omnira-Key-ID", c.keyID)
	resp, err := w.client.Do(req)
	if err != nil {
		w.fail(ctx, c, 0, classify(err))
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10)) // what the receiver answers is never read for content
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		w.complete(ctx, c, true, resp.StatusCode, "", nil, false)
		deliveryMetric.Inc("delivered")
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		w.fail(ctx, c, resp.StatusCode, "redirect")
	default:
		w.fail(ctx, c, resp.StatusCode, "http_status")
	}
}

// fail schedules the next attempt, or sends the delivery to the dead-letter list when the window is over.
func (w *Worker) fail(ctx context.Context, c claimed, status int, class string) {
	first := w.now()
	if c.firstAttempt != nil {
		first = *c.firstAttempt
	}
	wait := Backoff(c.attempts, w.jitter())
	next := w.now().Add(wait)
	if next.Sub(first) > w.Window {
		w.complete(ctx, c, false, status, class, nil, true)
		deliveryMetric.Inc("dead")
		return
	}
	w.complete(ctx, c, false, status, class, &next, false)
	deliveryMetric.Inc(class)
	deliveryMetric.Inc("retry")
}

func classify(err error) string {
	switch {
	case errors.Is(err, netpolicy.ErrBlockedAddress):
		return "blocked_address"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	if msg := err.Error(); containsAny(msg, "tls:", "x509:", "certificate") {
		return "tls"
	}
	return "connect"
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}

// stillVisible re-asks, at the moment of sending, what the events API asks at the moment of reading.
func (w *Worker) stillVisible(ctx context.Context, c claimed) (bool, error) {
	var visible bool
	err := platformdb.WithSystemTenantSession(ctx, w.pool, c.tenantID, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, w.pool)
		al, err := adapters.LoadAllowlistsFor(ctx, q, c.tenantID, c.installationID)
		if err != nil {
			if errors.Is(err, adapters.ErrInstallationInactive) {
				visible = false
				return nil
			}
			return err
		}
		var active *bool
		if err := q.QueryRow(ctx, `SELECT webhook_subscription_active($1, $2)`, c.tenantID, c.subscriptionID).Scan(&active); err != nil {
			return err
		}
		if active == nil { // deleted meanwhile
			visible = false
			return nil
		}
		if !*active {
			return errPaused
		}
		v := adapters.Visibility{TenantID: c.tenantID, QueueIDs: al.QueueIDs, ChannelIDs: al.ChannelIDs}
		visible, err = adapters.EventVisible(ctx, q, v, c.event.Aggregate.Type, c.event.Aggregate.ID, c.event.ConversationID)
		return err
	})
	return visible, err
}

// NewSecret makes a signing secret: 32 random bytes, returned hex-encoded with the prefix receivers see (whsec_...), plus the public key id.
func NewSecret() (secret string, keyID string, raw []byte, err error) {
	raw = make([]byte, 32)
	if _, err = readRandom(raw); err != nil {
		return "", "", nil, err
	}
	id := make([]byte, 6)
	if _, err = readRandom(id); err != nil {
		return "", "", nil, err
	}
	return "whsec_" + hex.EncodeToString(raw), "whk_" + hex.EncodeToString(id), raw, nil
}
