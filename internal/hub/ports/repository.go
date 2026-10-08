package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/hub/domain"
)

// HubRepository provides access to Hub data
type HubRepository interface {
	// ServiceHub operations
	GetServiceHubByID(ctx context.Context, hubID uuid.UUID) (*domain.ServiceHub, error)
	CreateServiceHub(ctx context.Context, hub *domain.ServiceHub) error

	// HubMembership operations
	GetHubMembership(ctx context.Context, hubID, userID uuid.UUID) (*domain.HubMembership, error)
	ListHubMemberships(ctx context.Context, hubID uuid.UUID) ([]*domain.HubMembership, error)
	CreateHubMembership(ctx context.Context, membership *domain.HubMembership) error

	// ServiceContract operations
	GetActiveServiceContract(ctx context.Context, hubID, tenantID uuid.UUID) (*domain.ServiceContract, error)
	GetServiceContract(ctx context.Context, contractID uuid.UUID) (*domain.ServiceContract, error)
	ListServiceContractsByHub(ctx context.Context, hubID uuid.UUID) ([]*domain.ServiceContract, error)
	ListServiceContractsByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.ServiceContract, error)
	CreateServiceContract(ctx context.Context, contract *domain.ServiceContract) error
	UpdateServiceContractStatus(ctx context.Context, contractID uuid.UUID, status string) error

	// EffectiveAccessGrant operations
	GetEffectiveGrant(ctx context.Context, hubID, userID, tenantID uuid.UUID) (*domain.EffectiveAccessGrant, error)
	ListEffectiveGrantsForUser(ctx context.Context, hubID, userID uuid.UUID) ([]*domain.EffectiveAccessGrant, error)
	ListEffectiveGrantsForTenant(ctx context.Context, hubID, tenantID uuid.UUID) ([]*domain.EffectiveAccessGrant, error)
	CreateEffectiveGrant(ctx context.Context, grant *domain.EffectiveAccessGrant) error
	InvalidateGrantsForContract(ctx context.Context, contractID uuid.UUID) error

	// HubInboxItem operations
	GetHubInboxItem(ctx context.Context, hubID, tenantID, conversationID uuid.UUID) (*domain.HubInboxItem, error)
	GetHubInboxItemByID(ctx context.Context, hubID, itemID uuid.UUID) (*domain.HubInboxItem, error)
	ListHubInboxItems(ctx context.Context, hubID uuid.UUID, onlyTenants []uuid.UUID, limit int, cursor string) ([]*domain.HubInboxItem, string, error)
	UpsertHubInboxItem(ctx context.Context, item *domain.HubInboxItem) error
	DeleteHubInboxItem(ctx context.Context, hubID, tenantID, conversationID uuid.UUID) error

	// WorkPool operations
	GetWorkPool(ctx context.Context, poolID uuid.UUID) (*domain.WorkPool, error)
	ListWorkPoolsByHub(ctx context.Context, hubID uuid.UUID) ([]*domain.WorkPool, error)
	CreateWorkPool(ctx context.Context, pool *domain.WorkPool) error

	// Skill operations
	GetSkill(ctx context.Context, skillID uuid.UUID) (*domain.Skill, error)
	ListSkillsByHub(ctx context.Context, hubID uuid.UUID) ([]*domain.Skill, error)
	CreateSkill(ctx context.Context, skill *domain.Skill) error

	// AgentSkill operations
	GetAgentSkill(ctx context.Context, userID, skillID uuid.UUID) (*domain.AgentSkill, error)
	ListAgentSkills(ctx context.Context, userID uuid.UUID) ([]*domain.AgentSkill, error)
	CreateAgentSkill(ctx context.Context, agentSkill *domain.AgentSkill) error
}
