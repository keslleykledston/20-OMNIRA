package domain

import (
	"time"

	"github.com/google/uuid"
)

// ServiceHub represents a service provider that can delegate work from multiple tenants
type ServiceHub struct {
	ID          uuid.UUID
	Name        string
	Description string
	Status      string // active | suspended
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// HubMembership represents a user's membership in a hub
type HubMembership struct {
	ID        uuid.UUID
	HubID     uuid.UUID
	UserID    uuid.UUID
	RoleID    uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ServiceContract represents delegation of a tenant to a hub
type ServiceContract struct {
	ID           uuid.UUID
	HubID        uuid.UUID
	TenantID     uuid.UUID
	Status       string // active | suspended | revoked
	ValidFrom    time.Time
	ValidUntil   *time.Time
	ServiceScope map[string]interface{} // JSON: queues, capabilities, etc.
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// EffectiveAccessGrant represents a cached grant of a Hub user to a Tenant
type EffectiveAccessGrant struct {
	ID                uuid.UUID
	HubID             uuid.UUID
	UserID            uuid.UUID
	TenantID          uuid.UUID
	ServiceContractID uuid.UUID
	WorkPoolID        *uuid.UUID
	CanReply          bool   // may claim and reply, not only read
	Status            string // active | suspended | revoked
	ValidFrom         time.Time
	ValidUntil        *time.Time
	GrantVersion      int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// HubInboxItem represents a unified inbox item for hub agents
type HubInboxItem struct {
	ID             uuid.UUID
	HubID          uuid.UUID
	TenantID       uuid.UUID
	ConversationID uuid.UUID
	QueueID        *uuid.UUID
	AssignedUserID *uuid.UUID
	CustomerName   string
	Channel        string
	Status         string
	Priority       string // low | normal | high | urgent
	SLADueAt       *time.Time
	LastActivityAt *time.Time
	UnreadCount    int
	MetadataJSON   map[string]interface{}
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// WorkPool groups agents within a hub by specialty
type WorkPool struct {
	ID          uuid.UUID
	HubID       uuid.UUID
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Skill represents a capability agents can have
type Skill struct {
	ID          uuid.UUID
	HubID       uuid.UUID
	Key         string
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// AgentSkill represents an agent's proficiency in a skill
type AgentSkill struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	SkillID     uuid.UUID
	Proficiency string // basic | intermediate | advanced
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
