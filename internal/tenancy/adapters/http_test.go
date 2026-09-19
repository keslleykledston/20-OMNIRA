package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/platform/authn"
	"github.com/omnira/omnira/internal/tenancy/application"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// MockTenantRepo — mock para testes.
type MockTenantRepo struct {
	tenants map[uuid.UUID]*domain.Tenant
}

func (m *MockTenantRepo) Store(ctx context.Context, tenant *domain.Tenant) error {
	m.tenants[tenant.ID] = tenant
	return nil
}

func (m *MockTenantRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	return m.tenants[id], nil
}

func (m *MockTenantRepo) FindAll(ctx context.Context, limit, offset int) ([]*domain.Tenant, error) {
	var result []*domain.Tenant
	for _, t := range m.tenants {
		result = append(result, t)
	}
	return result, nil
}

func (m *MockTenantRepo) Update(ctx context.Context, tenant *domain.Tenant) error {
	m.tenants[tenant.ID] = tenant
	return nil
}

// MockMembershipRepo — mock para testes.
type MockMembershipRepo struct {
	memberships map[uuid.UUID]*domain.Membership
}

func (m *MockMembershipRepo) Store(ctx context.Context, membership *domain.Membership) error {
	m.memberships[membership.ID] = membership
	return nil
}

func (m *MockMembershipRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Membership, error) {
	return m.memberships[id], nil
}

func (m *MockMembershipRepo) FindByTenantAndUser(ctx context.Context, tenantID, userID uuid.UUID) ([]*domain.Membership, error) {
	var result []*domain.Membership
	for _, mem := range m.memberships {
		if mem.TenantID == tenantID && mem.UserID == userID {
			result = append(result, mem)
		}
	}
	return result, nil
}

func (m *MockMembershipRepo) FindByUser(ctx context.Context, userID uuid.UUID) ([]*domain.Membership, error) {
	var result []*domain.Membership
	for _, mem := range m.memberships {
		if mem.UserID == userID && mem.IsActive() {
			result = append(result, mem)
		}
	}
	return result, nil
}

func (m *MockMembershipRepo) FindByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.Membership, error) {
	var result []*domain.Membership
	for _, mem := range m.memberships {
		if mem.TenantID == tenantID {
			result = append(result, mem)
		}
	}
	return result, nil
}

func (m *MockMembershipRepo) Update(ctx context.Context, membership *domain.Membership) error {
	m.memberships[membership.ID] = membership
	return nil
}

func (m *MockMembershipRepo) Delete(ctx context.Context, id uuid.UUID) error {
	delete(m.memberships, id)
	return nil
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc, err := domain.FromContext(r.Context())
		if err != nil {
			http.Error(w, "tenant_context not found", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
		_ = tc
	})
}

func TestAuthorizationMiddleware_MissingPrincipal(t *testing.T) {
	memberRepo := &MockMembershipRepo{memberships: make(map[uuid.UUID]*domain.Membership)}
	tenantRepo := &MockTenantRepo{tenants: make(map[uuid.UUID]*domain.Tenant)}
	authzSvc := application.NewAuthorizationService(memberRepo, tenantRepo)

	tenantID := uuid.New()

	// Request SEM Principal no context
	req := httptest.NewRequest("GET", "/api/v1/tenants/"+tenantID.String(), nil)

	// Middleware + handler
	handler := AuthorizationMiddleware(nil, authzSvc)(okHandler())

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Esperado: 401 (unauthorized)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

func TestAuthorizationMiddleware_MissingTenantID(t *testing.T) {
	memberRepo := &MockMembershipRepo{memberships: make(map[uuid.UUID]*domain.Membership)}
	tenantRepo := &MockTenantRepo{tenants: make(map[uuid.UUID]*domain.Tenant)}
	authzSvc := application.NewAuthorizationService(memberRepo, tenantRepo)

	// Criar principal
	userID := uuid.New()
	principal := &authn.Principal{UserID: userID, Subject: "user@example.com"}
	ctx := authn.WithPrincipal(context.Background(), principal)

	// Request SEM tenant_id no path (simular com valor vazio)
	req := httptest.NewRequest("GET", "/api/v1/tenants/", nil).WithContext(ctx)

	// Middleware + handler
	handler := AuthorizationMiddleware(nil, authzSvc)(okHandler())

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Esperado: 400 (tenant_id required)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}
