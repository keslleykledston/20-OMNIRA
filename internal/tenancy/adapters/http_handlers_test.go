package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/tenancy/application"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// MockRoleRepository — mock para RoleRepository.
type MockRoleRepository struct {
	roles map[uuid.UUID]*domain.Role
}

func (m *MockRoleRepository) FindByKey(ctx context.Context, key string) (*domain.Role, error) {
	for _, r := range m.roles {
		if r.Key == key {
			return r, nil
		}
	}
	return nil, nil
}

func (m *MockRoleRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Role, error) {
	return m.roles[id], nil
}

func (m *MockRoleRepository) FindAll(ctx context.Context) ([]*domain.Role, error) {
	var result []*domain.Role
	for _, r := range m.roles {
		result = append(result, r)
	}
	return result, nil
}

func TestGetTenantMe(t *testing.T) {
	memberRepo := &MockMembershipRepo{memberships: make(map[uuid.UUID]*domain.Membership)}
	tenantRepo := &MockTenantRepo{tenants: make(map[uuid.UUID]*domain.Tenant)}
	roleRepo := &MockRoleRepository{roles: make(map[uuid.UUID]*domain.Role)}

	tenantSvc := application.NewTenantService(tenantRepo)
	membershipSvc := application.NewMembershipService(memberRepo, roleRepo)
	handler := NewTenantAPIHandler(tenantSvc, membershipSvc)

	// Setup: tenant no contexto
	tenantID := uuid.New()
	tenant, _ := domain.NewTenant("Company A", domain.IsolationSharedStrong)
	tenant.ID = tenantID
	tenantRepo.Store(context.Background(), tenant)

	// Setup: TenantContext
	tc, _ := domain.NewTenantContext(tenantID, uuid.New(), domain.AccessSourceDirect)
	ctx := domain.WithTenantContext(context.Background(), tc)

	// Request
	req := httptest.NewRequest("GET", "/api/v1/tenants/me", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetTenantMe(w, req)

	// Validar resposta
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp TenantResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ID != tenantID {
		t.Errorf("expected tenant_id %s, got %s", tenantID, resp.ID)
	}
	if resp.LegalName != "Company A" {
		t.Errorf("expected legal_name 'Company A', got %s", resp.LegalName)
	}
}

func TestListMemberships(t *testing.T) {
	memberRepo := &MockMembershipRepo{memberships: make(map[uuid.UUID]*domain.Membership)}
	tenantRepo := &MockTenantRepo{tenants: make(map[uuid.UUID]*domain.Tenant)}
	roleRepo := &MockRoleRepository{roles: make(map[uuid.UUID]*domain.Role)}

	tenantSvc := application.NewTenantService(tenantRepo)
	membershipSvc := application.NewMembershipService(memberRepo, roleRepo)
	handler := NewTenantAPIHandler(tenantSvc, membershipSvc)

	// Setup: tenant e memberships
	tenantID := uuid.New()
	tenant, _ := domain.NewTenant("Company A", domain.IsolationSharedStrong)
	tenant.ID = tenantID
	tenantRepo.Store(context.Background(), tenant)

	// Criar 2 memberships
	for i := 0; i < 2; i++ {
		userID := uuid.New()
		roleID := uuid.New()
		membership, _ := domain.NewMembership(tenantID, userID, roleID)
		memberRepo.Store(context.Background(), membership)
	}

	// Setup: TenantContext
	tc, _ := domain.NewTenantContext(tenantID, uuid.New(), domain.AccessSourceDirect)
	ctx := domain.WithTenantContext(context.Background(), tc)

	// Request
	req := httptest.NewRequest("GET", "/api/v1/tenants/"+tenantID.String()+"/memberships", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListMemberships(w, req)

	// Validar resposta
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var memberships []MembershipResponse
	json.NewDecoder(w.Body).Decode(&memberships)
	if len(memberships) != 2 {
		t.Errorf("expected 2 memberships, got %d", len(memberships))
	}
}

func TestCreateMembership(t *testing.T) {
	memberRepo := &MockMembershipRepo{memberships: make(map[uuid.UUID]*domain.Membership)}
	tenantRepo := &MockTenantRepo{tenants: make(map[uuid.UUID]*domain.Tenant)}

	roleRepo := &MockRoleRepository{roles: make(map[uuid.UUID]*domain.Role)}

	tenantSvc := application.NewTenantService(tenantRepo)
	membershipSvc := application.NewMembershipService(memberRepo, roleRepo)
	handler := NewTenantAPIHandler(tenantSvc, membershipSvc)

	// Setup: tenant
	tenantID := uuid.New()
	tenant, _ := domain.NewTenant("Company A", domain.IsolationSharedStrong)
	tenant.ID = tenantID
	tenantRepo.Store(context.Background(), tenant)

	// Setup: role válida
	roleID := uuid.New()
	role := &domain.Role{ID: roleID, TenantID: nil, Key: "admin", Name: "Administrator"}
	roleRepo.roles[roleID] = role

	// Setup: TenantContext
	tc, _ := domain.NewTenantContext(tenantID, uuid.New(), domain.AccessSourceDirect)
	ctx := domain.WithTenantContext(context.Background(), tc)

	// Request body
	userID := uuid.New()
	reqBody := CreateMembershipRequest{UserID: userID, RoleID: roleID}
	bodyBytes, _ := json.Marshal(reqBody)

	// Request
	req := httptest.NewRequest("POST", "/api/v1/tenants/"+tenantID.String()+"/memberships",
		bytes.NewReader(bodyBytes)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.CreateMembership(w, req)

	// Validar resposta
	if w.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", w.Code)
	}

	var resp MembershipResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.UserID != userID {
		t.Errorf("expected user_id %s, got %s", userID, resp.UserID)
	}
}

func TestRevokeMembership_ServiceOnly(t *testing.T) {
	memberRepo := &MockMembershipRepo{memberships: make(map[uuid.UUID]*domain.Membership)}
	roleRepo := &MockRoleRepository{roles: make(map[uuid.UUID]*domain.Role)}

	membershipSvc := application.NewMembershipService(memberRepo, roleRepo)

	// Setup: tenant e membership
	tenantID := uuid.New()
	userID := uuid.New()
	roleID := uuid.New()
	membership, _ := domain.NewMembership(tenantID, userID, roleID)
	memberRepo.Store(context.Background(), membership)

	// Revogar membership via serviço
	err := membershipSvc.RevokeMembership(context.Background(), membership.ID)
	if err != nil {
		t.Fatalf("expected revoke to succeed, got error: %v", err)
	}

	// Validar que membership foi revogada
	retrieved, _ := memberRepo.FindByID(context.Background(), membership.ID)
	if retrieved.IsActive() {
		t.Error("expected membership to be revoked")
	}
}

func TestCreateMembership_MissingTenantContext(t *testing.T) {
	memberRepo := &MockMembershipRepo{memberships: make(map[uuid.UUID]*domain.Membership)}
	tenantRepo := &MockTenantRepo{tenants: make(map[uuid.UUID]*domain.Tenant)}

	roleRepo := &MockRoleRepository{roles: make(map[uuid.UUID]*domain.Role)}

	tenantSvc := application.NewTenantService(tenantRepo)
	membershipSvc := application.NewMembershipService(memberRepo, roleRepo)
	handler := NewTenantAPIHandler(tenantSvc, membershipSvc)

	// Request sem TenantContext
	req := httptest.NewRequest("POST", "/api/v1/tenants/xxx/memberships", nil).WithContext(context.Background())
	w := httptest.NewRecorder()

	handler.CreateMembership(w, req)

	// Validar resposta
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", w.Code)
	}
}
