package adapters

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/integrations/domain"
	"github.com/omnira/omnira/internal/integrations/ports"
)

// MetaAdapter bridges Meta/Facebook Channel API to Integration Gateway
type MetaAdapter struct {
	tenantID       uuid.UUID
	connectionID   uuid.UUID
	businessID     string // Meta Business Account ID
	pageID         string // Facebook Page ID
	credReference  string // vault path
}

func NewMetaAdapter(tenantID, connectionID uuid.UUID, businessID, pageID, credRef string) *MetaAdapter {
	return &MetaAdapter{
		tenantID:      tenantID,
		connectionID:  connectionID,
		businessID:    businessID,
		pageID:        pageID,
		credReference: credRef,
	}
}

func (a *MetaAdapter) Provider() string {
	return "Meta"
}

func (a *MetaAdapter) GetInstance(ctx context.Context) (*domain.IntegrationInstance, error) {
	return &domain.IntegrationInstance{
		ID:              a.connectionID,
		TenantID:        a.tenantID,
		Provider:        "Meta",
		IntegrationType: "channel",
		Status:          "active",
		Environment:     "prod",
		ConfigurationMetadata: map[string]interface{}{
			"business_id": a.businessID,
			"page_id":     a.pageID,
		},
		CredentialReference: a.credReference,
	}, nil
}

func (a *MetaAdapter) GetCapabilities() []string {
	return []string{
		"MESSAGE_SEND",
		"MESSAGE_RECEIVE",
		"MESSAGE_MEDIA",
		"CONTACT_SYNC",
		"WEBHOOK_RECEIVE",
	}
}

func (a *MetaAdapter) TestConnection(ctx context.Context) error {
	if a.credReference == "" {
		return fmt.Errorf("Meta credential not configured")
	}
	// TODO: Call Meta Graph API to test access
	return nil
}

func (a *MetaAdapter) Migrate(ctx context.Context, repo ports.IntegrationRepository) error {
	instance, err := a.GetInstance(ctx)
	if err != nil {
		return fmt.Errorf("failed to get Meta instance: %w", err)
	}

	existing, err := repo.GetIntegrationInstance(ctx, a.connectionID)
	if err != nil {
		return fmt.Errorf("failed to check existing: %w", err)
	}

	if existing != nil {
		return nil
	}

	if err := repo.CreateIntegrationInstance(ctx, instance); err != nil {
		return fmt.Errorf("failed to create Meta instance: %w", err)
	}

	for _, cap := range a.GetCapabilities() {
		if err := repo.CreateCapability(ctx, &domain.IntegrationCapability{
			ID:                    uuid.New(),
			IntegrationInstanceID: instance.ID,
			Capability:            cap,
			Enabled:               true,
		}); err != nil {
			return fmt.Errorf("failed to create capability %s: %w", cap, err)
		}
	}

	return nil
}
