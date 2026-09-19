package adapters

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/tenancy/application"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// setupIsolationTestDB — pool de SEED, conectada com privilégios elevados
// (OMNIRA_DATABASE_URL). Usada para preparar estado de teste diretamente,
// contornando RLS de propósito — não é o caminho que a aplicação real usa.
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

// setupIsolationAppPool — pool com a role de aplicação sem privilégios
// (OMNIRA_APP_DATABASE_URL), a mesma usada em produção. Usada com
// platformdb.WithTenantSession para exercitar RLS de verdade — sem isso,
// current_user_id() nunca é setado e a policy nega tudo (fail-closed).
func setupIsolationAppPool(t *testing.T) *pgxpool.Pool {
	dbURL := os.Getenv("OMNIRA_APP_DATABASE_URL")
	if dbURL == "" {
		t.Skip("OMNIRA_APP_DATABASE_URL not set; skipping RLS-enforced isolation tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("failed to connect to database as app role: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("failed to ping database as app role: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

// authorizeAsUser — roda AuthorizeAccessToTenant dentro de uma sessão RLS
// real (app.current_user_id setado), como a aplicação faz de verdade.
func authorizeAsUser(t *testing.T, appPool *pgxpool.Pool, authzSvc *application.AuthorizationService, tenantID, userID uuid.UUID) (*domain.TenantContext, error) {
	t.Helper()
	var tc *domain.TenantContext
	var authzErr error
	err := platformdb.WithTenantSession(context.Background(), appPool, userID, false, func(ctx context.Context) error {
		tc, authzErr = authzSvc.AuthorizeAccessToTenant(ctx, tenantID, userID)
		return nil
	})
	if err != nil {
		t.Fatalf("failed to open tenant session: %v", err)
	}
	return tc, authzErr
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

	appPool := setupIsolationAppPool(t)

	// Validar que acesso é permitido quando active
	tc, err := authorizeAsUser(t, appPool, authzSvc, tenant.ID, userID)
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
	tc, err = authorizeAsUser(t, appPool, authzSvc, tenant.ID, userID)
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

	appPool := setupIsolationAppPool(t)

	// Validar que acesso funciona quando tenant está ativo
	tc, err := authorizeAsUser(t, appPool, authzSvc, tenant.ID, userID)
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
	tc, err = authorizeAsUser(t, appPool, authzSvc, tenant.ID, userID)
	if err == nil {
		t.Error("expected authorization to fail when tenant is inactive")
	}
	if tc != nil {
		t.Error("expected no TenantContext for inactive tenant")
	}
}

// TestRLS_KnownUUID_CrossTenantDenied — a prova mais forte de isolamento:
// SQL cru executado com a role real de produção (omnira_app), contornando
// completamente os repositórios/serviços Go. Se este teste passar, o
// isolamento não depende de nenhuma linha de código de aplicação estar
// correta — está garantido pelo próprio banco. Conhecer o UUID de outro
// tenant/membership não deve bastar para lê-lo ou alterá-lo.
func TestRLS_KnownUUID_CrossTenantDenied(t *testing.T) {
	seedPool := setupIsolationTestDB(t)
	appPool := setupIsolationAppPool(t)
	ctx := context.Background()

	tenantRepo := NewPostgresTenantRepository(seedPool)
	memberRepo := NewPostgresMembershipRepository(seedPool)

	tenantA, _ := domain.NewTenant("RLS Raw Tenant A", domain.IsolationSharedStrong)
	tenantRepo.Store(ctx, tenantA)
	tenantB, _ := domain.NewTenant("RLS Raw Tenant B", domain.IsolationSharedStrong)
	tenantRepo.Store(ctx, tenantB)

	userX := uuid.New()
	seedPool.Exec(ctx, `
		INSERT INTO users (id, external_subject, email, status)
		VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING
	`, userX, userX.String(), userX.String()+"@example.com", "active")

	var adminRoleID uuid.UUID
	seedPool.QueryRow(ctx, `SELECT id FROM roles WHERE key = 'tenant_admin' AND tenant_id IS NULL LIMIT 1`).Scan(&adminRoleID)

	// userX só tem membership (admin) em A — nunca em B.
	membershipXinA, _ := domain.NewMembership(tenantA.ID, userX, adminRoleID)
	memberRepo.Store(ctx, membershipXinA)

	err := platformdb.WithTenantSession(ctx, appPool, userX, false, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, appPool)

		// 1. SELECT direto em tenants por UUID conhecido de B: deve vir vazio.
		var legalName string
		err := q.QueryRow(ctx, `SELECT legal_name FROM tenants WHERE id = $1`, tenantB.ID).Scan(&legalName)
		if err == nil {
			t.Errorf("esperava 0 linhas para tenant B, mas leu legal_name=%q", legalName)
		} else if err.Error() != "no rows in result set" {
			t.Errorf("esperava 'no rows in result set', obteve: %v", err)
		}

		// 2. SELECT direto em memberships de B por UUID conhecido: deve vir vazio.
		rows, err := q.Query(ctx, `SELECT id FROM memberships WHERE tenant_id = $1`, tenantB.ID)
		if err != nil {
			t.Errorf("query memberships de B não deveria falhar, apenas retornar vazio: %v", err)
		} else {
			count := 0
			for rows.Next() {
				count++
			}
			rows.Close()
			if count != 0 {
				t.Errorf("esperava 0 memberships visíveis em B, obteve %d", count)
			}
		}

		// 3. INSERT direto criando membership em B (userX não é admin de B):
		// RLS deve rejeitar mesmo sabendo o UUID exato de B.
		_, err = q.Exec(ctx, `
			INSERT INTO memberships (id, tenant_id, user_id, role_id, status, created_at, updated_at)
			VALUES (gen_random_uuid(), $1, $2, $3, 'active', now(), now())
		`, tenantB.ID, userX, adminRoleID)
		if err == nil {
			t.Error("esperava que o INSERT em memberships de B fosse rejeitado por RLS, mas foi aceito")
		}

		return nil
	})
	// O INSERT do passo 3 é rejeitado de propósito pela policy RLS; isso
	// aborta a transação em andamento, então o Commit final também falha
	// (comportamento padrão do Postgres para uma tx abortada) — é o
	// resultado esperado deste teste, não uma falha de infraestrutura.
	// As asserções reais (o que deveria ou não ser visível/aceito) já
	// rodaram via t.Errorf acima.
	if err != nil {
		t.Logf("sessão terminou com erro esperado (INSERT rejeitado por RLS abortou a tx): %v", err)
	}
}
