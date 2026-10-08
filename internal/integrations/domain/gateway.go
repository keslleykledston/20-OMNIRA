package domain

import (
	"time"

	"github.com/google/uuid"
)

// IntegrationInstance represents a unique connection to an external provider per Tenant
type IntegrationInstance struct {
	ID                    uuid.UUID
	TenantID              uuid.UUID
	Provider              string // e.g., "K3G", "Salesforce", "Zendesk"
	IntegrationType       string // channel | crm | billing | nms | ticketing
	Status                string // active | inactive | error
	Environment           string // prod | sandbox
	ConfigurationMetadata map[string]interface{}
	CredentialReference   string // vault path (never plaintext secret)
	CreatedAt             time.Time
	UpdatedAt             time.Time
	LastErrorAt           *time.Time
}

// IntegrationCapability represents fine-grained permission per instance
type IntegrationCapability struct {
	ID                    uuid.UUID
	IntegrationInstanceID uuid.UUID
	Capability            string // e.g., "CUSTOMER_READ", "TICKET_CREATE"
	Enabled               bool
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// ExternalActionReceipt tracks external writes for idempotency + audit (ADR-0029)
type ExternalActionReceipt struct {
	ID                       uuid.UUID
	TenantID                 uuid.UUID
	IntegrationInstanceID    uuid.UUID
	ActionType               string // e.g., "CREATE_TICKET", "CREATE_SERVICE_ORDER"
	ExternalID               string // provider's resource ID
	Status                   string // pending | in_flight | success | failure | reconciling
	ActorUserID              uuid.UUID
	HubID                    *uuid.UUID
	ConversationID           *uuid.UUID
	CorrelationID            string
	IdempotencyKey           string
	SanitizedRequestMetadata map[string]interface{}
	SanitizedResponseMetadata map[string]interface{}
	CreatedAt                time.Time
	ConfirmedAt              *time.Time
	UpdatedAt                time.Time
}

// WebhookDeduplication prevents replay attacks (ADR-0031)
type WebhookDeduplication struct {
	ID                    uuid.UUID
	IntegrationInstanceID uuid.UUID
	ProviderEventID       string
	ProcessedAt           time.Time
}
