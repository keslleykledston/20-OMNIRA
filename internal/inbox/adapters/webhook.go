package adapters

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	channelports "github.com/omnira/omnira/internal/channels/ports"
	inboxapp "github.com/omnira/omnira/internal/inbox/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// WebhookIntake owns the atomic boundary between provider deduplication and
// canonical persistence. A failed ingest rolls back the reservation so the
// provider can safely retry.
type WebhookIntake struct {
	pool    *pgxpool.Pool
	events  channelports.WebhookEventStore
	inbound *inboxapp.InboundService
}

func NewWebhookIntake(pool *pgxpool.Pool, events channelports.WebhookEventStore, inbound *inboxapp.InboundService) *WebhookIntake {
	return &WebhookIntake{pool: pool, events: events, inbound: inbound}
}

func (i *WebhookIntake) ProcessWebhook(ctx context.Context, connection channeldomain.ChannelConnection, deduplicationKey, eventType, payloadDigest string, message *channeldomain.InboundMessage) (bool, error) {
	if i == nil || i.pool == nil || i.events == nil || i.inbound == nil {
		return false, errors.New("inbox: webhook intake is not configured")
	}
	var duplicate bool
	err := platformdb.WithSystemTenantSession(ctx, i.pool, connection.TenantID, func(scoped context.Context) error {
		var err error
		duplicate, err = i.events.MarkReceived(scoped, connection, deduplicationKey, eventType, payloadDigest)
		if err != nil || duplicate || message == nil {
			return err
		}
		_, err = i.inbound.Ingest(scoped, connection, *message)
		return err
	})
	return duplicate, err
}
