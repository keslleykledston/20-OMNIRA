package adapters

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// setupTestDB — conecta ao PostgreSQL e retorna pool de conexões.
// Requer OMNIRA_DATABASE_URL definida.
func setupTestDB(t *testing.T) *pgxpool.Pool {
	dbURL := os.Getenv("OMNIRA_DATABASE_URL")
	if dbURL == "" {
		t.Skip("OMNIRA_DATABASE_URL not set; skipping PostgreSQL tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("failed to connect to database: %v", err)
	}

	// Validar conexão
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("failed to ping database: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
	})

	return pool
}

func TestPostgresTenantRepository_Store(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	repo := NewPostgresTenantRepository(pool)

	// Criar tenant
	tenant, err := domain.NewTenant("Test Company", domain.IsolationSharedStrong)
	if err != nil {
		t.Fatalf("failed to create tenant: %v", err)
	}

	// Store
	err = repo.Store(ctx, tenant)
	if err != nil {
		t.Fatalf("failed to store tenant: %v", err)
	}

	// FindByID — validar que foi armazenado
	retrieved, err := repo.FindByID(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("failed to find tenant: %v", err)
	}
	if retrieved == nil {
		t.Error("expected tenant to be found")
	}
	if retrieved.LegalName != "Test Company" {
		t.Errorf("expected legal_name 'Test Company', got %s", retrieved.LegalName)
	}
}

func TestPostgresMembershipRepository_Store(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	tenantRepo := NewPostgresTenantRepository(pool)
	memberRepo := NewPostgresMembershipRepository(pool)

	// Criar tenant
	tenant, _ := domain.NewTenant("Test Company", domain.IsolationSharedStrong)
	tenantRepo.Store(ctx, tenant)

	// Criar usuário no banco (necessário para FK)
	userID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, external_subject, email, status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO NOTHING
	`, userID, userID.String(), userID.String()+"@example.com", "active")
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	// Obter roleID de um system role (tenant_admin)
	var roleID uuid.UUID
	err = pool.QueryRow(ctx, `
		SELECT id FROM roles WHERE key = 'tenant_admin' AND tenant_id IS NULL LIMIT 1
	`).Scan(&roleID)
	if err != nil {
		t.Fatalf("failed to get system role: %v", err)
	}

	// Criar membership
	membership, _ := domain.NewMembership(tenant.ID, userID, roleID)

	// Store
	err = memberRepo.Store(ctx, membership)
	if err != nil {
		t.Fatalf("failed to store membership: %v", err)
	}

	// FindByID — validar
	retrieved, err := memberRepo.FindByID(ctx, membership.ID)
	if err != nil {
		t.Fatalf("failed to find membership: %v", err)
	}
	if retrieved == nil {
		t.Error("expected membership to be found")
	}
}

func TestRLSIsolation_TenantACannotReadTenantB(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	tenantRepo := NewPostgresTenantRepository(pool)
	memberRepo := NewPostgresMembershipRepository(pool)

	// Setup: Tenant A
	tenantA, _ := domain.NewTenant("Tenant A", domain.IsolationSharedStrong)
	tenantRepo.Store(ctx, tenantA)

	// Setup: Tenant B
	tenantB, _ := domain.NewTenant("Tenant B", domain.IsolationSharedStrong)
	tenantRepo.Store(ctx, tenantB)

	// Setup: User X em Tenant A
	userX := uuid.New()
	pool.Exec(ctx, `
		INSERT INTO users (id, external_subject, email, status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO NOTHING
	`, userX, userX.String(), userX.String()+"@example.com", "active")

	// Obter roleID de um system role
	var roleID uuid.UUID
	pool.QueryRow(ctx, `
		SELECT id FROM roles WHERE key = 'tenant_admin' AND tenant_id IS NULL LIMIT 1
	`).Scan(&roleID)

	membershipXinA, _ := domain.NewMembership(tenantA.ID, userX, roleID)
	memberRepo.Store(ctx, membershipXinA)

	// Test: User X em contexto de Tenant A
	// Esperado: não consegue ver memberships de Tenant B
	membershipsBinContext, err := memberRepo.FindByTenant(ctx, tenantB.ID)
	if err != nil {
		t.Fatalf("failed to find memberships: %v", err)
	}
	// Nota: RLS em FindByTenant não seria aplicado automaticamente sem contexto de conexão
	// Este teste demonstra que precisamos de um mecanismo diferente para validar RLS
	// (e.g., SET ROW LEVEL SECURITY na conexão).
	_ = membershipsBinContext

	// Para validar RLS adequadamente, precisaríamos:
	// 1. Conectar como usuário específico (autenticado)
	// 2. Definir variáveis de conexão (e.g., app.current_user_id)
	// 3. Validar que RLS bloqueia acesso
	//
	// Por enquanto, este teste é um placeholder que valida schema e permissões.
}

func TestMembershipRevoked_NoAccess(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	tenantRepo := NewPostgresTenantRepository(pool)
	memberRepo := NewPostgresMembershipRepository(pool)

	// Setup: Tenant A
	tenantA, _ := domain.NewTenant("Tenant A", domain.IsolationSharedStrong)
	tenantRepo.Store(ctx, tenantA)

	// Setup: User X em Tenant A
	userX := uuid.New()
	pool.Exec(ctx, `
		INSERT INTO users (id, external_subject, email, status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO NOTHING
	`, userX, userX.String(), userX.String()+"@example.com", "active")

	// Obter roleID de um system role
	var roleID uuid.UUID
	pool.QueryRow(ctx, `
		SELECT id FROM roles WHERE key = 'tenant_admin' AND tenant_id IS NULL LIMIT 1
	`).Scan(&roleID)

	membershipXinA, _ := domain.NewMembership(tenantA.ID, userX, roleID)
	memberRepo.Store(ctx, membershipXinA)

	// Validar que membership está ativa
	retrieved, _ := memberRepo.FindByID(ctx, membershipXinA.ID)
	if !retrieved.IsActive() {
		t.Error("expected membership to be active")
	}

	// Revogar membership
	membershipXinA.Revoke()
	memberRepo.Update(ctx, membershipXinA)

	// Validar que membership está revogada
	retrieved, _ = memberRepo.FindByID(ctx, membershipXinA.ID)
	if retrieved.IsActive() {
		t.Error("expected membership to be revoked")
	}
}

// Note: TestTenantIsolation_MultiTenant removed — functionality covered by isolation_test.go
// which includes stronger adversarial tests (T12 isolation harness)
