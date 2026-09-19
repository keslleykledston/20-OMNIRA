package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewTenant(t *testing.T) {
	// Test: sucesso
	tenant, err := NewTenant("Company A", IsolationSharedStrong)
	if err != nil {
		t.Fatalf("expected valid tenant, got error: %v", err)
	}
	if tenant.LegalName != "Company A" {
		t.Errorf("expected legal_name 'Company A', got %s", tenant.LegalName)
	}
	if tenant.Status != TenantStatusActive {
		t.Errorf("expected status active, got %s", tenant.Status)
	}

	// Test: sem legal_name
	_, err = NewTenant("", IsolationSharedStrong)
	if err == nil {
		t.Error("expected error for empty legal_name")
	}
}

func TestTenantDeactivate(t *testing.T) {
	tenant, _ := NewTenant("Company A", IsolationSharedStrong)

	// Test: deactivate activo
	err := tenant.Deactivate()
	if err != nil {
		t.Fatalf("expected deactivate to succeed, got error: %v", err)
	}
	if tenant.Status != TenantStatusInactive {
		t.Errorf("expected status inactive, got %s", tenant.Status)
	}

	// Test: não pode deactivate suspended
	tenant2, _ := NewTenant("Company B", IsolationSharedStrong)
	tenant2.Suspend()
	err = tenant2.Deactivate()
	if err == nil {
		t.Error("expected error when deactivating suspended tenant")
	}
}

func TestTenantSuspend(t *testing.T) {
	tenant, _ := NewTenant("Company A", IsolationSharedStrong)

	err := tenant.Suspend()
	if err != nil {
		t.Fatalf("expected suspend to succeed, got error: %v", err)
	}
	if tenant.Status != TenantStatusSuspended {
		t.Errorf("expected status suspended, got %s", tenant.Status)
	}
}

func TestTenantIsActive(t *testing.T) {
	tenant, _ := NewTenant("Company A", IsolationSharedStrong)

	if !tenant.IsActive() {
		t.Error("expected tenant to be active")
	}

	tenant.Deactivate()
	if tenant.IsActive() {
		t.Error("expected tenant to not be active after deactivate")
	}
}

func TestNewMembership(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	roleID := uuid.New()

	// Test: sucesso
	membership, err := NewMembership(tenantID, userID, roleID)
	if err != nil {
		t.Fatalf("expected valid membership, got error: %v", err)
	}
	if membership.Status != MembershipStatusActive {
		t.Errorf("expected status active, got %s", membership.Status)
	}

	// Test: tenant_id nulo
	_, err = NewMembership(uuid.Nil, userID, roleID)
	if err == nil {
		t.Error("expected error for nil tenant_id")
	}

	// Test: user_id nulo
	_, err = NewMembership(tenantID, uuid.Nil, roleID)
	if err == nil {
		t.Error("expected error for nil user_id")
	}

	// Test: role_id nulo
	_, err = NewMembership(tenantID, userID, uuid.Nil)
	if err == nil {
		t.Error("expected error for nil role_id")
	}
}

func TestMembershipRevoke(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	roleID := uuid.New()

	membership, _ := NewMembership(tenantID, userID, roleID)

	err := membership.Revoke()
	if err != nil {
		t.Fatalf("expected revoke to succeed, got error: %v", err)
	}
	if membership.Status != MembershipStatusRevoked {
		t.Errorf("expected status revoked, got %s", membership.Status)
	}

	// Test: não pode deactivate revoked
	err = membership.Deactivate()
	if err == nil {
		t.Error("expected error when deactivating revoked membership")
	}
}

func TestMembershipIsActive(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	roleID := uuid.New()

	membership, _ := NewMembership(tenantID, userID, roleID)

	if !membership.IsActive() {
		t.Error("expected membership to be active")
	}

	membership.Revoke()
	if membership.IsActive() {
		t.Error("expected membership to not be active after revoke")
	}
}
