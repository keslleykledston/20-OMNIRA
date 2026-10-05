package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
)

// ChannelConnectionRepository — persistência de ChannelConnection.
// ErrDuplicateConnection: the provider's external number id is already connected (unique per provider).
var ErrDuplicateConnection = errors.New("channel: connection already exists for this provider number")

type ChannelConnectionRepository interface {
	Store(ctx context.Context, conn *domain.ChannelConnection) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.ChannelConnection, error)

	// FindByExternalNumberID — a fonte confiável para resolver qual Tenant
	// dono de um webhook inbound (ex.: phone_number_id do path/query da
	// Meta). NUNCA resolver tenant a partir de um campo dentro do corpo do
	// payload — isso é exatamente o tipo de bug que o audit do donor
	// project documenta (issue #236: campo de tenant scope opcional
	// permitiu credencial cruzada).
	FindByExternalNumberID(ctx context.Context, provider string, externalNumberID string) (*domain.ChannelConnection, error)

	FindByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.ChannelConnection, error)
	Update(ctx context.Context, conn *domain.ChannelConnection) error
}

// WebhookEventStore reserves one provider event/message key per connection.
// MarkReceived returns true only for the first delivery; payloads are never
// persisted here, only a short digest for audit/debug correlation.
type WebhookEventStore interface {
	MarkReceived(ctx context.Context, connection domain.ChannelConnection, providerEventID, eventType, payloadDigest string) (duplicate bool, err error)
}
