package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/integrations/domain"
)

// IntegrationRepository provides access to Integration Gateway data
type IntegrationRepository interface {
	// IntegrationInstance operations
	GetIntegrationInstance(ctx context.Context, id uuid.UUID) (*domain.IntegrationInstance, error)
	ListIntegrationsByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.IntegrationInstance, error)
	ListIntegrationsByType(ctx context.Context, tenantID uuid.UUID, integrationType string) ([]*domain.IntegrationInstance, error)
	CreateIntegrationInstance(ctx context.Context, instance *domain.IntegrationInstance) error
	UpdateIntegrationInstance(ctx context.Context, instance *domain.IntegrationInstance) error

	// IntegrationCapability operations
	GetCapability(ctx context.Context, instanceID uuid.UUID, capability string) (*domain.IntegrationCapability, error)
	ListCapabilities(ctx context.Context, instanceID uuid.UUID) ([]*domain.IntegrationCapability, error)
	CreateCapability(ctx context.Context, cap *domain.IntegrationCapability) error
	EnableCapability(ctx context.Context, instanceID uuid.UUID, capability string) error
	DisableCapability(ctx context.Context, instanceID uuid.UUID, capability string) error

	// ExternalActionReceipt operations (idempotency + audit)
	GetReceiptByIdempotencyKey(ctx context.Context, instanceID uuid.UUID, key string) (*domain.ExternalActionReceipt, error)
	CreateReceipt(ctx context.Context, receipt *domain.ExternalActionReceipt) error
	UpdateReceiptStatus(ctx context.Context, receiptID uuid.UUID, status, externalID string) error

	// Webhook deduplication
	CheckDuplicate(ctx context.Context, instanceID uuid.UUID, providerEventID string) (bool, error)
	MarkProcessed(ctx context.Context, instanceID uuid.UUID, providerEventID string) error
}

// ConnectorAdapter bridges old Channel Connection system to new Integration Gateway
type ConnectorAdapter interface {
	// Provider returns provider name (e.g., "WAHA", "Meta", "K3G")
	Provider() string

	// GetInstance returns the equivalent IntegrationInstance
	GetInstance(ctx context.Context) (*domain.IntegrationInstance, error)

	// GetCapabilities returns available capabilities for this connector
	GetCapabilities() []string

	// TestConnection validates the connector is working
	TestConnection(ctx context.Context) error

	// Migrate converts legacy connection to IntegrationInstance
	Migrate(ctx context.Context, repo IntegrationRepository) error
}

// IntegrationExecutor executes external actions via Integration Gateway
type IntegrationExecutor interface {
	// ExecuteAction sends a command to external provider
	// Never accepts tenant_id or credentials from caller; always uses instance context
	ExecuteAction(ctx context.Context, receipt *domain.ExternalActionReceipt) (string, error) // returns external_id
}
