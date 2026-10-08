package adapters

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/integrations/domain"
	"github.com/omnira/omnira/internal/integrations/ports"
)

// WAHAAdapter bridges legacy WAHA channel connections to Integration Gateway
type WAHAAdapter struct {
	tenantID      uuid.UUID
	connectionID  uuid.UUID // legacy channel_connections.id
	sessionID     string    // WAHA session ID
	phoneNumber   string    // WhatsApp phone
	credReference string    // vault path to WAHA credentials
}

func NewWAHAAdapter(tenantID, connectionID uuid.UUID, sessionID, phone, credRef string) *WAHAAdapter {
	return &WAHAAdapter{
		tenantID:      tenantID,
		connectionID:  connectionID,
		sessionID:     sessionID,
		phoneNumber:   phone,
		credReference: credRef,
	}
}

func (a *WAHAAdapter) Provider() string {
	return "WAHA"
}

func (a *WAHAAdapter) GetInstance(ctx context.Context) (*domain.IntegrationInstance, error) {
	return &domain.IntegrationInstance{
		ID:                  a.connectionID,
		TenantID:            a.tenantID,
		Provider:            "WAHA",
		IntegrationType:     "channel",
		Status:              "active",
		Environment:         "prod",
		ConfigurationMetadata: map[string]interface{}{
			"session_id":   a.sessionID,
			"phone_number": a.phoneNumber,
		},
		CredentialReference: a.credReference,
	}, nil
}

func (a *WAHAAdapter) GetCapabilities() []string {
	return []string{
		"MESSAGE_SEND",
		"MESSAGE_RECEIVE",
		"MESSAGE_MEDIA",
		"CONTACT_SYNC",
		"PRESENCE_TRACKING",
	}
}

func (a *WAHAAdapter) TestConnection(ctx context.Context) error {
	// TODO: Call actual WAHA API to test session
	// For now, stub: assume it works if credReference is set
	if a.credReference == "" {
		return fmt.Errorf("WAHA credential not configured")
	}
	return nil
}

func (a *WAHAAdapter) Migrate(ctx context.Context, repo ports.IntegrationRepository) error {
	// Convert legacy channel_connections row to IntegrationInstance
	instance, err := a.GetInstance(ctx)
	if err != nil {
		return fmt.Errorf("failed to get WAHA instance: %w", err)
	}

	// Create in Integration Gateway (or update if already exists)
	existing, err := repo.GetIntegrationInstance(ctx, a.connectionID)
	if err != nil {
		return fmt.Errorf("failed to check existing: %w", err)
	}

	if existing != nil {
		// Already migrated
		return nil
	}

	if err := repo.CreateIntegrationInstance(ctx, instance); err != nil {
		return fmt.Errorf("failed to create WAHA instance: %w", err)
	}

	// Create capabilities
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
