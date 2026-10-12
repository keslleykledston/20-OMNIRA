// Package realtime bridges Postgres NOTIFY (emitted by row triggers, migration 000026) to NATS
// subjects consumed by the SSE endpoint. Realtime is best-effort and ephemeral: if the bridge is
// down, events are lost and clients recover by refetching through the REST API.
package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/opsmetrics"
)

// A NOTIFY is only a hint: the subject it is published on carries the tenant, and the SSE endpoint trusts the subject (it subscribes to the tenant it authorized).
// So the bridge does not take the (tenant, conversation) pair from the payload on faith: it asks the database that the conversation belongs to that tenant (a pair
// that never changes once true, so a verified pair is remembered). An unverifiable pair is dropped. Without this, anything able to run pg_notify could make
// the events (identifiers, statuses) of one company's conversation appear on another company's live stream.
var droppedMetric = opsmetrics.NewCounter("omnira_realtime_bridge_dropped_total", "Notifications the realtime bridge refused to publish, by reason", "reason", "malformed", "unverified_pair", "verify_error")

const verifiedCap = 20000

const Channel = "omnira_inbox_events"

// Publisher is the NATS subset the bridge needs.
type Publisher interface {
	Publish(subject string, data []byte) error
}

type Bridge struct {
	pool *pgxpool.Pool
	nc   Publisher
	// Backoff bounds for reconnecting the dedicated LISTEN connection.
	MinBackoff, MaxBackoff time.Duration
	// verify answers "does this conversation belong to this tenant?"; nil (no database: unit tests only) accepts every pair.
	verify func(ctx context.Context, tenant, conversation uuid.UUID) (bool, error)
	mu     sync.Mutex
	known  map[[2]uuid.UUID]struct{}
}

var _ Publisher = (*nats.Conn)(nil)

func NewBridge(pool *pgxpool.Pool, nc Publisher) *Bridge {
	b := &Bridge{pool: pool, nc: nc, MinBackoff: time.Second, MaxBackoff: 15 * time.Second, known: map[[2]uuid.UUID]struct{}{}}
	if pool != nil {
		b.verify = func(ctx context.Context, tenant, conversation uuid.UUID) (bool, error) {
			var ok bool
			err := platformdb.WithSystemTenantSession(ctx, pool, tenant, func(c context.Context) error {
				return platformdb.QuerierFromContext(c, pool).QueryRow(c, `SELECT EXISTS (SELECT 1 FROM conversations WHERE tenant_id = $1 AND id = $2)`, tenant, conversation).Scan(&ok)
			})
			return ok, err
		}
	}
	return b
}

// pairBelongs is true when the conversation is the tenant's. A positive answer is remembered (bounded); a negative or failed one is not.
func (b *Bridge) pairBelongs(ctx context.Context, tenant, conversation uuid.UUID) bool {
	if b.verify == nil {
		return true
	}
	key := [2]uuid.UUID{tenant, conversation}
	b.mu.Lock()
	_, hit := b.known[key]
	b.mu.Unlock()
	if hit {
		return true
	}
	ok, err := b.verify(ctx, tenant, conversation)
	if err != nil {
		droppedMetric.Inc("verify_error")
		log.Printf("realtime bridge: could not verify a notification (dropped): %v", err)
		return false
	}
	if !ok {
		droppedMetric.Inc("unverified_pair")
		log.Printf("realtime bridge: dropping a notification whose conversation does not belong to its tenant")
		return false
	}
	b.mu.Lock()
	if len(b.known) >= verifiedCap {
		b.known = map[[2]uuid.UUID]struct{}{}
	}
	b.known[key] = struct{}{}
	b.mu.Unlock()
	return true
}

type notification struct {
	TenantID       uuid.UUID       `json:"tenant_id"`
	ConversationID uuid.UUID       `json:"conversation_id"`
	Type           string          `json:"type"`
	Data           json.RawMessage `json:"data"`
}

// Subject is where the SSE endpoint subscribes: tenant-scoped, then conversation.
func Subject(tenant, conversation uuid.UUID) string {
	return fmt.Sprintf("inbox.events.%s.%s", tenant, conversation)
}

// Run listens until ctx is cancelled, reconnecting with exponential backoff.
func (b *Bridge) Run(ctx context.Context) {
	backoff := b.MinBackoff
	for ctx.Err() == nil {
		started := time.Now()
		err := b.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		// A connection that stayed healthy for a while resets the backoff.
		if time.Since(started) > 30*time.Second {
			backoff = b.MinBackoff
		}
		log.Printf("realtime bridge: listen ended (%v); reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > b.MaxBackoff {
			backoff = b.MaxBackoff
		}
	}
}

func (b *Bridge) listen(ctx context.Context) error {
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
		return err
	}
	log.Printf("realtime bridge: listening on %s", Channel)
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			// Do not return a possibly broken connection to the pool.
			_ = conn.Conn().Close(context.Background())
			return err
		}
		b.forward(ctx, n.Payload)
	}
}

func (b *Bridge) forward(ctx context.Context, payload string) {
	var n notification
	if err := json.Unmarshal([]byte(payload), &n); err != nil || n.TenantID == uuid.Nil || n.ConversationID == uuid.Nil || n.Type == "" {
		droppedMetric.Inc("malformed")
		log.Printf("realtime bridge: dropping malformed notification")
		return
	}
	if !b.pairBelongs(ctx, n.TenantID, n.ConversationID) {
		return
	}
	body, err := json.Marshal(map[string]any{
		"event_id":  uuid.NewString(), // unique per event: clients de-duplicate with it (it is also the SSE `id:`)
		"v":         1,                // payload version
		"type":      n.Type,
		"id":        n.ConversationID.String(),
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"data":      n.Data,
	})
	if err != nil {
		return
	}
	if err := b.nc.Publish(Subject(n.TenantID, n.ConversationID), body); err != nil {
		log.Printf("realtime bridge: publish failed: %v", err)
	}
}
