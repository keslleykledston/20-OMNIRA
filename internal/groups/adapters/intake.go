// Package adapters implements ADR-0015: read-only WhatsApp group messages, kept apart from
// contacts/conversations/messages. A group stores messages only while an administrator has
// enabled it; every other group is dropped before anything is persisted.
package adapters

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	channelports "github.com/omnira/omnira/internal/channels/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// DefaultMaxBytes caps the size of the group tables on the local disk (1 GiB). The archive job normally
// keeps them far below it; the cap only matters when the external disk stays unreachable for a long time.
const DefaultMaxBytes int64 = 1 << 30

const sizeCheckEvery = time.Minute

// Intake is the webhook-side half: it decides, inside a tenant system session derived from the
// resolved connection (never from the payload), whether a group message is stored.
type Intake struct {
	pool    *pgxpool.Pool
	events  channelports.WebhookEventStore
	counter metric.Int64Counter

	maxBytes int64 // <= 0 disables the cap
	now      func() time.Time
	mu       sync.Mutex
	checked  time.Time
	over     bool
}

func NewIntake(pool *pgxpool.Pool, events channelports.WebhookEventStore) *Intake {
	counter, _ := otel.Meter("omnira/groups").Int64Counter("group_message_total")
	return &Intake{pool: pool, events: events, counter: counter, maxBytes: DefaultMaxBytes, now: time.Now}
}

// WithMaxBytes sets the size cap for the group tables; zero or negative disables it.
func (i *Intake) WithMaxBytes(n int64) *Intake {
	i.maxBytes = n
	return i
}

// overLimit reports whether the group tables exceed the cap. The size is read at most once a minute,
// so a burst of group traffic does not turn into a burst of catalog queries. When the cap is hit only
// group storage pauses (messages are dropped and counted); nothing already stored is deleted, and 1:1
// conversations are unaffected.
func (i *Intake) overLimit(ctx context.Context) bool {
	if i.maxBytes <= 0 {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.checked.IsZero() && i.now().Sub(i.checked) < sizeCheckEvery {
		return i.over
	}
	var size int64
	err := platformdb.QuerierFromContext(ctx, i.pool).QueryRow(ctx,
		`SELECT pg_total_relation_size('wa_group_messages') + pg_total_relation_size('wa_groups') + pg_total_relation_size('wa_group_archive_batches')`).Scan(&size)
	if err != nil {
		return i.over // keep the last known answer; a failed size read must not stop ingestion
	}
	i.checked, i.over = i.now(), size > i.maxBytes
	if i.over {
		log.Printf("groups: storage paused, group tables use %d bytes (cap %d); archive to the external disk is not catching up", size, i.maxBytes)
	}
	return i.over
}

// ProcessGroupMessage returns ingested=true when the message was stored, duplicate=true when the
// provider event was already seen, and both false when the group is unknown or not enabled
// (dropped: not even the dedup record is written, so nothing about a group nobody enabled persists).
func (i *Intake) ProcessGroupMessage(ctx context.Context, conn channeldomain.ChannelConnection, dedupKey, eventType, digest string, msg channeldomain.InboundGroupMessage) (ingested, duplicate bool, err error) {
	if i == nil || i.pool == nil || i.events == nil {
		return false, false, errors.New("groups: intake is not configured")
	}
	if i.overLimit(ctx) {
		if i.counter != nil {
			i.counter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "paused_size_limit")))
		}
		return false, false, nil
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
		// Who wrote it, in the provider's own terms (an opaque id, never a phone): the same participant row serves topics
		// and, later, handoff. A message without a usable author id is stored anyway.
		var senderID *uuid.UUID
		if msg.AuthorJID != "" {
			id, err := channeladapters.UpsertChannelParticipant(scoped, q, channeladapters.ParticipantRef{
				TenantID: conn.TenantID, ConnectionID: conn.ID, Provider: string(conn.Provider), ExternalID: msg.AuthorJID, DisplayName: msg.AuthorName,
			})
			if err != nil {
				return err
			}
			senderID = &id
		}
		tag, err := q.Exec(scoped, `
			INSERT INTO wa_group_messages
			  (tenant_id, group_id, provider_message_id, author_jid, author_name, from_me, message_type, body, sent_at,
			   sender_channel_participant_id, reply_to_external_message_id, reply_to_group_message_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,
			  CASE WHEN $11 = '' THEN NULL ELSE (
			    SELECT g.id FROM wa_group_messages g
			    WHERE g.tenant_id = $1 AND g.group_id = $2 AND (g.provider_message_id = $11 OR split_part(g.provider_message_id, '_', 3) = $11)
			    ORDER BY g.sent_at DESC LIMIT 1) END)
			ON CONFLICT (tenant_id, group_id, provider_message_id) DO NOTHING`,
			conn.TenantID, groupID, msg.ProviderMessageID, msg.AuthorJID, msg.AuthorName, msg.FromMe, msg.Type, msg.Text, msg.SentAt,
			senderID, msg.ReplyToExternalID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			ingested = true
			if _, err = q.Exec(scoped, `
				UPDATE wa_groups SET last_message_at = GREATEST(COALESCE(last_message_at, '-infinity'::timestamptz), $3), updated_at = now()
				WHERE tenant_id = $1 AND id = $2`, conn.TenantID, groupID, msg.SentAt); err != nil {
				return err
			}
			// ADR-0018: each participant is classified individually; the group's kind follows (internal / customer_service /
			// external_other / unclassified, never a guess).
			if senderID != nil {
				_, err = q.Exec(scoped, `SELECT recompute_group_kind($1, $2)`, conn.TenantID, groupID)
			}
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
