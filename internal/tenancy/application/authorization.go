package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/tenancy/ports"
)

// Sentinel errors — permitem errors.Is() no chamador (ex.: middleware HTTP)
// mapear cada falha para o status code correto. errors.New(...) solto não
// funciona com errors.Is porque cada chamada cria uma instância distinta.
var (
	ErrInvalidTenantID  = errors.New("invalid tenant_id")
	ErrInvalidActorID   = errors.New("invalid actor_id")
	ErrTenantNotFound   = errors.New("tenant not found")
	ErrTenantNotActive  = errors.New("tenant is not active")
	ErrNoActiveMembership = errors.New("access denied: no active membership")
)

// AuthorizationService — valida acesso a recursos tenant-bound.
type AuthorizationService struct {
	memberRepo ports.MembershipRepository
	tenantRepo ports.TenantRepository
}

// NewAuthorizationService — cria um novo AuthorizationService.
func NewAuthorizationService(
	memberRepo ports.MembershipRepository,
	tenantRepo ports.TenantRepository,
) *AuthorizationService {
	return &AuthorizationService{
		memberRepo: memberRepo,
		tenantRepo: tenantRepo,
	}
}

// AuthorizeAccessToTenant — valida se um actor pode acessar um tenant.
// Retorna TenantContext se autorizado, erro caso contrário.
//
// Regra: o actor deve ter membership ATIVO no tenant.
// Nenhum valor do request é aceito como autoridade.
func (s *AuthorizationService) AuthorizeAccessToTenant(
	ctx context.Context,
	tenantID uuid.UUID,
	actorID uuid.UUID,
) (*domain.TenantContext, error) {
	// Validar UUIDs
	if tenantID == uuid.Nil {
		return nil, ErrInvalidTenantID
	}
	if actorID == uuid.Nil {
		return nil, ErrInvalidActorID
	}

	// Verificar que o tenant existe
	tenant, err := s.tenantRepo.FindByID(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch tenant: %w", err)
	}
	if tenant == nil {
		return nil, ErrTenantNotFound
	}

	// Verificar se o tenant está ativo
	if !tenant.IsActive() {
		return nil, ErrTenantNotActive
	}

	// Verificar membership: actor MUST ter membership ATIVO no tenant
	memberships, err := s.memberRepo.FindByTenantAndUser(ctx, tenantID, actorID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch memberships: %w", err)
	}

	// Buscar membership ativa
	var activeMembership *domain.Membership
	for _, m := range memberships {
		if m.IsActive() {
			activeMembership = m
			break
		}
	}

	if activeMembership == nil {
		return nil, ErrNoActiveMembership
	}

	// Criar TenantContext
	tenantContext, err := domain.NewTenantContext(tenantID, actorID, domain.AccessSourceDirect)
	if err != nil {
		return nil, fmt.Errorf("failed to create tenant context: %w", err)
	}

	return tenantContext, nil
}

// IsAuthorized — verifica se um actor tem membership ativo num tenant.
// Retorna true se autorizado, false caso contrário.
func (s *AuthorizationService) IsAuthorized(
	ctx context.Context,
	tenantID uuid.UUID,
	actorID uuid.UUID,
) bool {
	_, err := s.AuthorizeAccessToTenant(ctx, tenantID, actorID)
	return err == nil
}
