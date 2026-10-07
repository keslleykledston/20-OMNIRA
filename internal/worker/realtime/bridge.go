// Package realtime bridges Postgres NOTIFY (emitted by row triggers, migration 000026) to NATS
// subjects consumed by the SSE endpoint. Realtime is best-effort and ephemeral: if the bridge is
// down, events are lost and clients recover by refetching through the REST API.
package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

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
}

var _ Publisher = (*nats.Conn)(nil)

func NewBridge(pool *pgxpool.Pool, nc Publisher) *Bridge {
	return &Bridge{pool: pool, nc: nc, MinBackoff: time.Second, MaxBackoff: 15 * time.Second}
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
		b.forward(n.Payload)
	}
}

func (b *Bridge) forward(payload string) {
	var n notification
	if err := json.Unmarshal([]byte(payload), &n); err != nil || n.TenantID == uuid.Nil || n.ConversationID == uuid.Nil || n.Type == "" {
		log.Printf("realtime bridge: dropping malformed notification")
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
