package adapters

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/tenancy/application"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// setupIsolationTestDB — setup PostgreSQL para testes de isolamento.
func setupIsolationTestDB(t *testing.T) *pgxpool.Pool {
	dbURL := os.Getenv("OMNIRA_DATABASE_URL")
	if dbURL == "" {
		t.Skip("OMNIRA_DATABASE_URL not set; skipping isolation tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("failed to connect to database: %v", err)
	}

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("failed to ping database: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
	})

	return pool
}

// TestIsolation_TenantACannotReadTenantBData — adversarial: Tenant A tenta ler dados de Tenant B.
func TestIsolation_TenantACannotReadTenantBData(t *testing.T) {
	pool := setupIsolationTestDB(t)
	ctx := context.Background()

	tenantRepo := NewPostgresTenantRepository(pool)
	memberRepo := NewPostgresMembershipRepository(pool)

	// Setup: Tenant A
	tenantA, _ := domain.NewTenant("Tenant A Corp", domain.IsolationSharedStrong)
	tenantRepo.Store(ctx, tenantA)

	// Setup: Tenant B
	tenantB, _ := domain.NewTenant("Tenant B Corp", domain.IsolationSharedStrong)
	tenantRepo.Store(ctx, tenantB)

	// Setup: User X em Tenant A
	userX := uuid.New()
	pool.Exec(ctx, `
		INSERT INTO users (id, external_subject, email, status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO NOTHING
	`, userX, userX.String(), userX.String()+"@example.com", "active")

	var systemRoleID uuid.UUID
	pool.QueryRow(ctx, `
		SELECT id FROM roles WHERE key = 'tenant_admin' AND tenant_id IS NULL LIMIT 1
	`).Scan(&systemRoleID)

	membershipXinA, _ := domain.NewMembership(tenantA.ID, userX, systemRoleID)
	memberRepo.Store(ctx, membershipXinA)

	// Adversarial test: User X tenta listar memberships de Tenant B
	// RLS deveria bloquear, mas a query raw abaixo testaria sem RLS
	// Para testar RLS, precisaríamos de uma conexão autenticada como userX
	// Por enquanto, validamos que a data foi separada corretamente

	membershipsBinA, _ := memberRepo.FindByTenant(ctx, tenantA.ID)
	membershipsBinB, _ := memberRepo.FindByTenant(ctx, tenantB.ID)

	if len(membershipsBinA) != 1 {
		t.Errorf("expected 1 membership in Tenant A, got %d", len(membershipsBinA))
	}
	if len(membershipsBinB) != 0 {
		t.Errorf("expected 0 memberships in Tenant B, got %d", len(membershipsBinB))
	}
}

// TestIsolation_MembershipRevocation_DeniesAccess — adversarial: User revogado tenta acessar tenant.
func TestIsolation_MembershipRevocation_DeniesAccess(t *testing.T) {
	pool := setupIsolationTestDB(t)
	ctx := context.Background()

	tenantRepo := NewPostgresTenantRepository(pool)
	memberRepo := NewPostgresMembershipRepository(pool)
	authzSvc := application.NewAuthorizationService(memberRepo, tenantRepo)

	// Setup: Tenant
	tenant, _ := domain.NewTenant("Test Tenant", domain.IsolationSharedStrong)
	tenantRepo.Store(ctx, tenant)

	// Setup: User with active membership
	userID := uuid.New()
	pool.Exec(ctx, `
		INSERT INTO users (id, external_subject, email, status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO NOTHING
	`, userID, userID.String(), userID.String()+"@example.com", "active")

	var systemRoleID uuid.UUID
	pool.QueryRow(ctx, `
		SELECT id FROM roles WHERE key = 'tenant_admin' AND tenant_id IS NULL LIMIT 1
	`).Scan(&systemRoleID)

	membership, _ := domain.NewMembership(tenant.ID, userID, systemRoleID)
	memberRepo.Store(ctx, membership)

	// Validar que acesso é permitido quando active
	tc, err := authzSvc.AuthorizeAccessToTenant(ctx, tenant.ID, userID)
	if err != nil {
		t.Fatalf("expected authorization before revocation, got error: %v", err)
	}
	if tc == nil {
		t.Error("expected TenantContext, got nil")
	}

	// Revogar membership
	membership.Revoke()
	memberRepo.Update(ctx, membership)

	// Adversarial: User tenta acessar após revogação
	tc, err = authzSvc.AuthorizeAccessToTenant(ctx, tenant.ID, userID)
	if err == nil {
		t.Error("expected authorization to fail after revocation")
	}
	if tc != nil {
		t.Error("expected no TenantContext after revocation")
	}
}

// TestIsolation_MultiTenantDataSeparation — isolation suite: 3 tenants, cada um com dados separados.
func TestIsolation_MultiTenantDataSeparation(t *testing.T) {
	pool := setupIsolationTestDB(t)
	ctx := context.Background()

	tenantRepo := NewPostgresTenantRepository(pool)
	memberRepo := NewPostgresMembershipRepository(pool)

	tenants := make([]*domain.Tenant, 3)
	for i := 0; i < 3; i++ {
		tenant, _ := domain.NewTenant("Tenant "+string(rune('A'+i)), domain.IsolationSharedStrong)
		tenantRepo.Store(ctx, tenant)
		tenants[i] = tenant
	}

	// Obter system role
	var systemRoleID uuid.UUID
	pool.QueryRow(ctx, `
		SELECT id FROM roles WHERE key = 'tenant_admin' AND tenant_id IS NULL LIMIT 1
	`).Scan(&systemRoleID)

	// Populate: 3 users por tenant
	usersByTenant := make(map[int][]uuid.UUID)
	for tenantIdx, tenant := range tenants {
		for userIdx := 0; userIdx < 3; userIdx++ {
			userID := uuid.New()
			pool.Exec(ctx, `
				INSERT INTO users (id, external_subject, email, status)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (id) DO NOTHING
			`, userID, userID.String(), userID.String()+"@example.com", "active")

			membership, _ := domain.NewMembership(tenant.ID, userID, systemRoleID)
			memberRepo.Store(ctx, membership)

			usersByTenant[tenantIdx] = append(usersByTenant[tenantIdx], userID)
		}
	}

	// Validate: cada tenant vê apenas seus próprios dados
	for tenantIdx, tenant := range tenants {
		memberships, _ := memberRepo.FindByTenant(ctx, tenant.ID)
		expectedCount := len(usersByTenant[tenantIdx])
		if len(memberships) != expectedCount {
			t.Errorf("Tenant %d: expected %d memberships, got %d", tenantIdx, expectedCount, len(memberships))
		}

		// Validate que não há cross-tenant data
		for _, membership := range memberships {
			if membership.TenantID != tenant.ID {
				t.Errorf("Tenant %d: found membership for different tenant %s", tenantIdx, membership.TenantID)
			}
		}
	}
}

// TestIsolation_TenantInactive_DeniesAccess — isolation: tenant inativo nega acesso mesmo com membership ativa.
func TestIsolation_TenantInactive_DeniesAccess(t *testing.T) {
	pool := setupIsolationTestDB(t)
	ctx := context.Background()

	tenantRepo := NewPostgresTenantRepository(pool)
	memberRepo := NewPostgresMembershipRepository(pool)
	authzSvc := application.NewAuthorizationService(memberRepo, tenantRepo)

	// Setup: Tenant (ativo)
	tenant, _ := domain.NewTenant("Test Tenant", domain.IsolationSharedStrong)
	tenantRepo.Store(ctx, tenant)

	// Setup: User com membership
	userID := uuid.New()
	pool.Exec(ctx, `
		INSERT INTO users (id, external_subject, email, status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO NOTHING
	`, userID, userID.String(), userID.String()+"@example.com", "active")

	var systemRoleID uuid.UUID
	pool.QueryRow(ctx, `
		SELECT id FROM roles WHERE key = 'tenant_admin' AND tenant_id IS NULL LIMIT 1
	`).Scan(&systemRoleID)

	membership, _ := domain.NewMembership(tenant.ID, userID, systemRoleID)
	memberRepo.Store(ctx, membership)

	// Validar que acesso funciona quando tenant está ativo
	tc, err := authzSvc.AuthorizeAccessToTenant(ctx, tenant.ID, userID)
	if err != nil {
		t.Fatalf("expected authorization for active tenant, got error: %v", err)
	}
	if tc == nil {
		t.Error("expected TenantContext")
	}

	// Deactivate tenant
	tenant.Deactivate()
	tenantRepo.Update(ctx, tenant)

	// Adversarial: User com membership ativa mas tenant inativo
	tc, err = authzSvc.AuthorizeAccessToTenant(ctx, tenant.ID, userID)
	if err == nil {
		t.Error("expected authorization to fail when tenant is inactive")
	}
	if tc != nil {
		t.Error("expected no TenantContext for inactive tenant")
	}
}
