package adapters

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/integrations/domain"
	"github.com/omnira/omnira/internal/integrations/ports"
)

// K3GAdapter bridges K3G ERP/CRM/OSS integration to Integration Gateway
type K3GAdapter struct {
	tenantID      uuid.UUID
	instanceID    uuid.UUID
	system        string // "ERP", "CRM", "OSS", "Billing", "NMS"
	credReference string // vault path
}

func NewK3GAdapter(tenantID, instanceID uuid.UUID, system, credRef string) *K3GAdapter {
	return &K3GAdapter{
		tenantID:      tenantID,
		instanceID:    instanceID,
		system:        system,
		credReference: credRef,
	}
}

func (a *K3GAdapter) Provider() string {
	return "K3G"
}

func (a *K3GAdapter) GetInstance(ctx context.Context) (*domain.IntegrationInstance, error) {
	integrationType := "crm" // default
	switch a.system {
	case "ERP":
		integrationType = "crm"
	case "OSS":
		integrationType = "nms"
	case "Billing":
		integrationType = "billing"
	case "NMS":
		integrationType = "nms"
	}

	return &domain.IntegrationInstance{
		ID:              a.instanceID,
		TenantID:        a.tenantID,
		Provider:        "K3G",
		IntegrationType: integrationType,
		Status:          "active",
		Environment:     "prod",
		ConfigurationMetadata: map[string]interface{}{
			"system": a.system,
		},
		CredentialReference: a.credReference,
	}, nil
}

func (a *K3GAdapter) GetCapabilities() []string {
	// Capabilities depend on system type
	switch a.system {
	case "ERP":
		return []string{
			"CUSTOMER_READ",
			"CONTRACT_READ",
			"TICKET_CREATE",
			"TICKET_UPDATE",
			"SERVICE_ORDER_CREATE",
		}
	case "OSS":
		return []string{
			"NETWORK_READ",
			"ONU_READ",
			"INCIDENT_CREATE",
			"INCIDENT_UPDATE",
			"DIAGNOSTIC_RUN",
		}
	case "Billing":
		return []string{
			"INVOICE_READ",
			"PAYMENT_READ",
			"PLAN_CHANGE",
			"PLAN_READ",
		}
	default:
		return []string{}
	}
}

func (a *K3GAdapter) TestConnection(ctx context.Context) error {
	if a.credReference == "" {
		return fmt.Errorf("K3G credential not configured")
	}
	// TODO: Call K3G API to test access
	return nil
}

func (a *K3GAdapter) Migrate(ctx context.Context, repo ports.IntegrationRepository) error {
	instance, err := a.GetInstance(ctx)
	if err != nil {
		return fmt.Errorf("failed to get K3G instance: %w", err)
	}

	existing, err := repo.GetIntegrationInstance(ctx, a.instanceID)
	if err != nil {
		return fmt.Errorf("failed to check existing: %w", err)
	}

	if existing != nil {
		return nil
	}

	if err := repo.CreateIntegrationInstance(ctx, instance); err != nil {
		return fmt.Errorf("failed to create K3G instance: %w", err)
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
