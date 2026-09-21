package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestHasPermission_Admin(t *testing.T) {
	adminRole := SystemRoles["tenant_admin"]

	if !adminRole.HasPermission(ResourceMembership, ActionAdmin) {
		t.Errorf("admin should have admin permission on membership")
	}

	if !adminRole.HasPermission(ResourceMembership, ActionWrite) {
		t.Errorf("admin with admin permission should have write")
	}

	if !adminRole.HasPermission(ResourceMembership, ActionRead) {
		t.Errorf("admin with admin permission should have read")
	}
}

func TestHasPermission_Viewer(t *testing.T) {
	viewerRole := SystemRoles["tenant_agent"]

	if viewerRole.HasPermission(ResourceMembership, ActionWrite) {
		t.Errorf("viewer should not have write permission on membership")
	}

	if viewerRole.HasPermission(ResourceMembership, ActionAdmin) {
		t.Errorf("viewer should not have admin permission")
	}

	if !viewerRole.HasPermission(ResourceTenant, ActionRead) {
		t.Errorf("viewer should have read permission on tenant")
	}
}

func TestCanRead(t *testing.T) {
	supervisorRole := SystemRoles["tenant_supervisor"]

	if !supervisorRole.CanRead(ResourceTenant) {
		t.Errorf("supervisor should be able to read tenant")
	}

	emptyRole := &Role{}
	if emptyRole.CanRead(ResourceTenant) {
		t.Errorf("role without permissions should not be able to read tenant")
	}
}

func TestCanWrite(t *testing.T) {
	adminRole := SystemRoles["tenant_admin"]

	if !adminRole.CanWrite(ResourceMembership) {
		t.Errorf("admin should be able to write membership")
	}

	agentRole := SystemRoles["tenant_agent"]
	if agentRole.CanWrite(ResourceMembership) {
		t.Errorf("agent should not be able to write membership")
	}
}

func TestCanAdmin(t *testing.T) {
	adminRole := SystemRoles["tenant_admin"]

	if !adminRole.CanAdmin(ResourceSettings) {
		t.Errorf("admin should be able to admin settings")
	}

	supervisorRole := SystemRoles["tenant_supervisor"]
	if supervisorRole.CanAdmin(ResourceSettings) {
		t.Errorf("supervisor should not be able to admin settings")
	}
}

func TestNewTenantRole(t *testing.T) {
	tenantID := uuid.New()
	perms := []*Permission{
		{Resource: ResourceTenant, Action: ActionRead},
	}

	role := NewTenantRole(tenantID, "custom", "Custom role", perms)

	if role.TenantID != tenantID {
		t.Errorf("expected tenant ID to be set")
	}

	if role.IsSystem {
		t.Errorf("tenant role should not be marked as system")
	}

	if role.Name != "custom" {
		t.Errorf("expected role name to be 'custom'")
	}

	if !role.CanRead(ResourceTenant) {
		t.Errorf("role should have read permission on tenant")
	}
}

func TestSystemRoles_All(t *testing.T) {
	expectedRoles := []string{"system_admin", "tenant_admin", "tenant_supervisor", "tenant_agent", "hub_admin"}

	for _, roleName := range expectedRoles {
		if _, ok := SystemRoles[roleName]; !ok {
			t.Errorf("system role '%s' not found", roleName)
		}
	}
}

func TestSystemRoles_Superadmin(t *testing.T) {
	sysAdminRole := SystemRoles["system_admin"]

	if !sysAdminRole.CanAdmin(ResourceTenant) {
		t.Errorf("system_admin should be able to admin tenant")
	}

	if !sysAdminRole.CanAdmin(ResourceMembership) {
		t.Errorf("system_admin should be able to admin membership")
	}

	if !sysAdminRole.CanAdmin(ResourceSettings) {
		t.Errorf("system_admin should be able to admin settings")
	}
}

func TestHasPermission_AdminDoesNotCrossResources(t *testing.T) {
	role := &Role{Permissions: []*Permission{{Resource: ResourceMembership, Action: ActionAdmin}}}

	if !role.HasPermission(ResourceMembership, ActionWrite) {
		t.Errorf("admin on membership should imply write on membership")
	}
	for _, other := range []PermissionResource{ResourceTenant, ResourceAudit, ResourceSettings} {
		if role.HasPermission(other, ActionRead) || role.HasPermission(other, ActionAdmin) {
			t.Errorf("admin on membership must not authorize %s", other)
		}
	}
}

func TestSystemRoles_NoUnexpectedCrossResourceGrant(t *testing.T) {
	// tenant_admin has no admin on tenant: must not write tenant via membership admin.
	if SystemRoles["tenant_admin"].HasPermission(ResourceTenant, ActionAdmin) {
		t.Errorf("tenant_admin must not have admin on tenant")
	}
	if SystemRoles["tenant_agent"].HasPermission(ResourceMembership, ActionRead) {
		t.Errorf("tenant_agent must not read membership")
	}
}

func TestHasPermission_Nil(t *testing.T) {
	var nilRole *Role
	if nilRole.HasPermission(ResourceTenant, ActionRead) {
		t.Errorf("nil role should not have any permissions")
	}
}
