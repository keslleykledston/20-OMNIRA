package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// MembershipStatus — estado de uma membership.
type MembershipStatus string

const (
	MembershipStatusActive  MembershipStatus = "active"
	MembershipStatusInactive MembershipStatus = "inactive"
	MembershipStatusRevoked MembershipStatus = "revoked"
)

// Role — papel de um usuário dentro de um tenant.
type Role struct {
	ID       uuid.UUID
	TenantID *uuid.UUID // nil = system role
	Key      string
	Name     string
}

// Membership — associação entre User e Tenant com um Role.
type Membership struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	UserID    uuid.UUID
	RoleID    uuid.UUID
	Status    MembershipStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewMembership — factory com validação.
func NewMembership(tenantID, userID, roleID uuid.UUID) (*Membership, error) {
	if tenantID == uuid.Nil {
		return nil, errors.New("tenant_id is required")
	}
	if userID == uuid.Nil {
		return nil, errors.New("user_id is required")
	}
	if roleID == uuid.Nil {
		return nil, errors.New("role_id is required")
	}

	return &Membership{
		ID:        uuid.New(),
		TenantID:  tenantID,
		UserID:    userID,
		RoleID:    roleID,
		Status:    MembershipStatusActive,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}, nil
}

// Deactivate — muda status pra inactive.
func (m *Membership) Deactivate() error {
	if m.Status == MembershipStatusRevoked {
		return errors.New("cannot deactivate a revoked membership")
	}
	m.Status = MembershipStatusInactive
	m.UpdatedAt = time.Now().UTC()
	return nil
}

// Revoke — muda status pra revoked (operação irreversível).
func (m *Membership) Revoke() error {
	m.Status = MembershipStatusRevoked
	m.UpdatedAt = time.Now().UTC()
	return nil
}

// IsActive — verdade se status == active.
func (m *Membership) IsActive() bool {
	return m.Status == MembershipStatusActive
}
