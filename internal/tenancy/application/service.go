package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/tenancy/ports"
)

// TenantService — lógica de negócio para Tenants.
type TenantService struct {
	repo ports.TenantRepository
}

// NewTenantService — cria um novo TenantService.
func NewTenantService(repo ports.TenantRepository) *TenantService {
	return &TenantService{repo: repo}
}

// CreateTenant — cria um novo Tenant.
func (s *TenantService) CreateTenant(ctx context.Context, legalName string, profile domain.IsolationProfile) (*domain.Tenant, error) {
	tenant, err := domain.NewTenant(legalName, profile)
	if err != nil {
		return nil, fmt.Errorf("failed to create tenant: %w", err)
	}

	if err := s.repo.Store(ctx, tenant); err != nil {
		return nil, fmt.Errorf("failed to store tenant: %w", err)
	}

	return tenant, nil
}

// GetTenant — busca um Tenant pelo ID.
func (s *TenantService) GetTenant(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	tenant, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch tenant: %w", err)
	}
	if tenant == nil {
		return nil, errors.New("tenant not found")
	}
	return tenant, nil
}

// ListTenants — lista Tenants.
func (s *TenantService) ListTenants(ctx context.Context, limit, offset int) ([]*domain.Tenant, error) {
	tenants, err := s.repo.FindAll(ctx, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list tenants: %w", err)
	}
	return tenants, nil
}

// MembershipService — lógica de negócio para Memberships.
type MembershipService struct {
	memberRepo ports.MembershipRepository
	roleRepo   ports.RoleRepository
}

// NewMembershipService — cria um novo MembershipService.
func NewMembershipService(memberRepo ports.MembershipRepository, roleRepo ports.RoleRepository) *MembershipService {
	return &MembershipService{
		memberRepo: memberRepo,
		roleRepo:   roleRepo,
	}
}

// GrantMembership — concede membership (tenant_id, user_id, role_id).
func (s *MembershipService) GrantMembership(ctx context.Context, tenantID, userID, roleID uuid.UUID) (*domain.Membership, error) {
	// Validar que o Role existe
	role, err := s.roleRepo.FindByID(ctx, roleID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch role: %w", err)
	}
	if role == nil {
		return nil, errors.New("role not found")
	}

	// Criar a membership
	membership, err := domain.NewMembership(tenantID, userID, roleID)
	if err != nil {
		return nil, fmt.Errorf("failed to create membership: %w", err)
	}

	if err := s.memberRepo.Store(ctx, membership); err != nil {
		return nil, fmt.Errorf("failed to store membership: %w", err)
	}

	return membership, nil
}

// GetMembership — busca uma Membership pelo ID.
func (s *MembershipService) GetMembership(ctx context.Context, id uuid.UUID) (*domain.Membership, error) {
	membership, err := s.memberRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch membership: %w", err)
	}
	if membership == nil {
		return nil, errors.New("membership not found")
	}
	return membership, nil
}

// GetUserMemberships — busca todas as memberships de um usuário em um tenant.
func (s *MembershipService) GetUserMemberships(ctx context.Context, tenantID, userID uuid.UUID) ([]*domain.Membership, error) {
	memberships, err := s.memberRepo.FindByTenantAndUser(ctx, tenantID, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch memberships: %w", err)
	}
	return memberships, nil
}

// RevokeMembership — revoga uma Membership (irreversível).
func (s *MembershipService) RevokeMembership(ctx context.Context, id uuid.UUID) error {
	membership, err := s.memberRepo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to fetch membership: %w", err)
	}
	if membership == nil {
		return errors.New("membership not found")
	}

	if err := membership.Revoke(); err != nil {
		return fmt.Errorf("failed to revoke membership: %w", err)
	}

	if err := s.memberRepo.Update(ctx, membership); err != nil {
		return fmt.Errorf("failed to update membership: %w", err)
	}

	return nil
}

// GetTenantMemberships — lista todas as memberships de um tenant.
func (s *MembershipService) GetTenantMemberships(ctx context.Context, tenantID uuid.UUID) ([]*domain.Membership, error) {
	memberships, err := s.memberRepo.FindByTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch memberships: %w", err)
	}
	return memberships, nil
}
