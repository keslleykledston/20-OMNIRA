package adapters

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	channelports "github.com/omnira/omnira/internal/channels/ports"
	inboxapp "github.com/omnira/omnira/internal/inbox/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// WebhookIntake owns the atomic boundary between provider deduplication and
// canonical persistence. A failed ingest rolls back the reservation so the
// provider can safely retry.
type WebhookIntake struct {
	pool    *pgxpool.Pool
	events  channelports.WebhookEventStore
	inbound *inboxapp.InboundService
	status  metric.Int64Counter
}

func NewWebhookIntake(pool *pgxpool.Pool, events channelports.WebhookEventStore, inbound *inboxapp.InboundService) *WebhookIntake {
	status, _ := otel.Meter("omnira/inbox").Int64Counter("delivery_status_receipt_total")
	return &WebhookIntake{pool: pool, events: events, inbound: inbound, status: status}
}

// receiptShape describes an id WITHOUT revealing it: prefix (true/false), kind of chat address and
// whether a bare id segment exists. Enough to see why a receipt did not match, no phone or id.
func receiptShape(id string) string {
	parts := strings.Split(id, "_")
	if len(parts) < 3 {
		return "other"
	}
	chat := "other"
	switch {
	case strings.HasSuffix(parts[1], "@c.us"):
		chat = "c.us"
	case strings.HasSuffix(parts[1], "@lid"):
		chat = "lid"
	case strings.HasSuffix(parts[1], "@g.us"):
		chat = "g.us"
	}
	return parts[0] + "/" + chat
}

func (i *WebhookIntake) ProcessWebhook(ctx context.Context, connection channeldomain.ChannelConnection, deduplicationKey, eventType, payloadDigest string, message *channeldomain.InboundMessage, status *channeldomain.DeliveryStatusUpdate) (bool, error) {
	if i == nil || i.pool == nil || i.events == nil || i.inbound == nil {
		return false, errors.New("inbox: webhook intake is not configured")
	}
	var duplicate bool
	err := platformdb.WithSystemTenantSession(ctx, i.pool, connection.TenantID, func(scoped context.Context) error {
		var err error
		duplicate, err = i.events.MarkReceived(scoped, connection, deduplicationKey, eventType, payloadDigest)
		if err != nil || duplicate {
			return err
		}
		if message != nil {
			_, err = i.inbound.Ingest(scoped, connection, *message)
		} else if status != nil {
			var applied bool
			applied, err = i.inbound.ApplyDeliveryStatus(scoped, connection, *status)
			if err == nil {
				outcome := "applied"
				if !applied {
					// Not necessarily a problem (an older state, a message from another channel), but a receipt
					// that matches nothing must be visible: say what shape it had, never the id itself.
					outcome = "unmatched"
					log.Printf("inbox: delivery receipt matched no outbound message (state=%s shape=%s)", status.State, receiptShape(status.ProviderMessageID))
				}
				if i.status != nil {
					i.status.Add(scoped, 1, metric.WithAttributes(attribute.String("outcome", outcome), attribute.String("state", string(status.State))))
				}
			}
		}
		return err
	})
	return duplicate, err
}
