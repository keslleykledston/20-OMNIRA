// Package adapters implements ADR-0015: read-only WhatsApp group messages, kept apart from
// contacts/conversations/messages. A group stores messages only while an administrator has
// enabled it; every other group is dropped before anything is persisted.
package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	channelports "github.com/omnira/omnira/internal/channels/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Intake is the webhook-side half: it decides, inside a tenant system session derived from the
// resolved connection (never from the payload), whether a group message is stored.
type Intake struct {
	pool    *pgxpool.Pool
	events  channelports.WebhookEventStore
	counter metric.Int64Counter
}

func NewIntake(pool *pgxpool.Pool, events channelports.WebhookEventStore) *Intake {
	counter, _ := otel.Meter("omnira/groups").Int64Counter("group_message_total")
	return &Intake{pool: pool, events: events, counter: counter}
}

// ProcessGroupMessage returns ingested=true when the message was stored, duplicate=true when the
// provider event was already seen, and both false when the group is unknown or not enabled
// (dropped: not even the dedup record is written, so nothing about a group nobody enabled persists).
func (i *Intake) ProcessGroupMessage(ctx context.Context, conn channeldomain.ChannelConnection, dedupKey, eventType, digest string, msg channeldomain.InboundGroupMessage) (ingested, duplicate bool, err error) {
	if i == nil || i.pool == nil || i.events == nil {
		return false, false, errors.New("groups: intake is not configured")
	}
	err = platformdb.WithSystemTenantSession(ctx, i.pool, conn.TenantID, func(scoped context.Context) error {
		q := platformdb.QuerierFromContext(scoped, i.pool)
		var groupID uuid.UUID
		scanErr := q.QueryRow(scoped, `
			SELECT id FROM wa_groups
			WHERE tenant_id = $1 AND channel_connection_id = $2 AND provider_group_id = $3 AND enabled`,
			conn.TenantID, conn.ID, msg.GroupJID).Scan(&groupID)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil
		}
		if scanErr != nil {
			return scanErr
		}
		dup, err := i.events.MarkReceived(scoped, conn, dedupKey, eventType, digest)
		if err != nil {
			return err
		}
		if dup {
			duplicate = true
			return nil
		}
		tag, err := q.Exec(scoped, `
			INSERT INTO wa_group_messages
			  (tenant_id, group_id, provider_message_id, author_jid, author_name, from_me, message_type, body, sent_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (tenant_id, group_id, provider_message_id) DO NOTHING`,
			conn.TenantID, groupID, msg.ProviderMessageID, msg.AuthorJID, msg.AuthorName, msg.FromMe, msg.Type, msg.Text, msg.SentAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			ingested = true
			_, err = q.Exec(scoped, `
				UPDATE wa_groups SET last_message_at = GREATEST(COALESCE(last_message_at, '-infinity'::timestamptz), $3), updated_at = now()
				WHERE tenant_id = $1 AND id = $2`, conn.TenantID, groupID, msg.SentAt)
			return err
		}
		duplicate = true
		return nil
	})
	if i.counter != nil {
		outcome := "dropped_not_enabled"
		switch {
		case err != nil:
			outcome = "error"
		case duplicate:
			outcome = "duplicate"
		case ingested:
			outcome = "ingested"
		}
		i.counter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
	return ingested, duplicate, err
}
