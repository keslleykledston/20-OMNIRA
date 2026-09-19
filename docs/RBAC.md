# OMNIRA Role-Based Access Control (RBAC)

**Release:** R0.2  
**Version:** 0.1.0

## Overview

OMNIRA implements role-based access control (RBAC) with:
- **5 system roles** (superadmin, admin, editor, viewer, guest)
- **Resource-based permissions** (tenant, membership, audit, settings)
- **Action scoping** (read, write, admin, delete)
- **Tenant-scoped custom roles** (create org-specific roles)

---

## System Roles

### 1. Superadmin
**Full platform access across all tenants.**

```
Permissions:
  - tenant:admin       (full tenant management)
  - membership:admin   (full membership management)
  - audit:admin        (view all audit logs)
  - settings:admin     (platform settings)

Use case: Platform operators, security team
```

### 2. Admin
**Full access within a tenant.**

```
Permissions:
  - tenant:read        (view tenant details)
  - membership:admin   (grant/revoke/manage memberships)
  - audit:read         (view audit logs for tenant)
  - settings:admin     (manage tenant settings)

Use case: Tenant administrators, ops leads
```

### 3. Editor
**Read and write access to business data.**

```
Permissions:
  - tenant:read        (view tenant)
  - membership:read    (view members)
  - audit:read         (view logs)

Use case: Content creators, team leads
```

### 4. Viewer
**Read-only access.**

```
Permissions:
  - tenant:read        (view tenant)
  - audit:read         (view logs)

Use case: Stakeholders, auditors, analysts
```

### 5. Guest
**Minimal access, no permissions.**

```
Permissions: (none)

Use case: Trial users, limited access
```

---

## Resources & Actions

### Resources
| Resource | Purpose |
|----------|---------|
| `tenant` | Tenant settings, metadata |
| `membership` | User memberships, roles, access |
| `audit` | Audit events, activity logs |
| `settings` | Tenant configuration, billing |

### Actions
| Action | Scope |
|--------|-------|
| `read` | View data (no modification) |
| `write` | Create and modify data |
| `admin` | Full control + grant/revoke access |
| `delete` | Remove data (future) |

### Permission Matrix

```
              tenant  membership  audit  settings
superadmin    admin     admin     admin    admin
admin         read      admin     read     admin
editor        read      read      read     -
viewer        read      -         read     -
guest         -         -         -        -
```

---

## Creating Custom Roles (Tenant-Scoped)

Tenants can create org-specific roles:

```go
import (
  "github.com/omnira/omnira/internal/rbac/application"
  "github.com/omnira/omnira/internal/rbac/domain"
)

// Create RBAC service
rbacSvc := application.NewRBACService(roleRepo)

// Create custom role: "data_analyst"
perms := []*domain.Permission{
  {Resource: domain.ResourceTenant, Action: domain.ActionRead},
  {Resource: domain.ResourceAudit, Action: domain.ActionRead},
}

role, err := rbacSvc.CreateTenantRole(
  ctx,
  tenantID,
  "data_analyst",
  "Read-only access for data analysis",
  perms,
)
```

### Best Practices

1. **Principle of Least Privilege:** Start with minimal permissions, add as needed
2. **Role Naming:** Use clear, descriptive names (e.g., `report_viewer`, `api_writer`)
3. **Permission Scoping:** Scope permissions to specific resources, not blanket access
4. **Avoid Role Proliferation:** Reuse system roles where possible

---

## Checking Permissions

### In Application Code

```go
// Get user's role
role, _ := rbacSvc.GetRole(ctx, userRoleID)

// Check specific permission
if rbacSvc.CanAdmin(role, domain.ResourceMembership) {
  // Allow membership operations
}

// Or use the shorthand
if role.CanWrite(domain.ResourceTenant) {
  // Allow writes
}
```

### In HTTP Handlers

```go
// Check permission before operation
if !rbacSvc.CanWrite(userRole, domain.ResourceTenant) {
  http.Error(w, "Forbidden", http.StatusForbidden)
  return
}

// Proceed with operation
```

### Middleware Pattern

```go
func RequirePermission(resource domain.PermissionResource, action domain.PermissionAction) http.Middleware {
  return func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
      tenantCtx := r.Context().Value("tenant_context").(*domain.TenantContext)
      userRole := r.Context().Value("user_role").(*domain.Role)

      if !rbacSvc.CheckPermission(userRole, resource, action) {
        http.Error(w, "Forbidden", http.StatusForbidden)
        return
      }

      next.ServeHTTP(w, r)
    })
  }
}

// Usage
router.HandleFunc("/settings", 
  RequirePermission(domain.ResourceSettings, domain.ActionAdmin)(handleSettings),
)
```

---

## Database Schema (R0.2+)

Once role management endpoints are added:

```sql
-- Roles table
CREATE TABLE roles (
  id UUID PRIMARY KEY,
  tenant_id UUID NOT NULL,  -- NULL for system roles
  name VARCHAR(100) NOT NULL,
  description TEXT,
  is_system BOOLEAN DEFAULT FALSE,
  created_at TIMESTAMP DEFAULT NOW(),
  UNIQUE(tenant_id, name) -- Role names unique per tenant
);

-- Permissions table
CREATE TABLE permissions (
  id UUID PRIMARY KEY,
  resource VARCHAR(50) NOT NULL,  -- tenant, membership, audit, settings
  action VARCHAR(50) NOT NULL,     -- read, write, admin, delete
  PRIMARY KEY(resource, action)
);

-- Role-Permission mapping
CREATE TABLE role_permissions (
  role_id UUID NOT NULL,
  permission_id UUID NOT NULL,
  PRIMARY KEY(role_id, permission_id),
  FOREIGN KEY(role_id) REFERENCES roles(id) ON DELETE CASCADE,
  FOREIGN KEY(permission_id) REFERENCES permissions(id)
);

-- Membership now includes role_id
ALTER TABLE memberships ADD role_id VARCHAR(100) DEFAULT 'viewer';
```

---

## Permission Check Flow

```
HTTP Request
    ↓
JWT Validation → Extract tenant_id + user_id
    ↓
Load Membership → user's role_id in this tenant
    ↓
Load Role → system role or custom role
    ↓
CheckPermission(role, resource, action)
    ↓
Allowed? → Continue : Return 403 Forbidden
```

---

## Audit & Logging

Every permission check should be logged:

```go
// Log permission decision
event := &audit.Event{
  TenantID: tenantCtx.TenantID,
  ActorID: tenantCtx.ActorID,
  Action: "permission_check",
  Resource: string(resource),
  Metadata: map[string]interface{}{
    "role_id": userRole.ID,
    "action": string(action),
    "allowed": allowed,
  },
}
auditSvc.RecordEvent(ctx, event)
```

---

## Future Enhancements (R0.3+)

1. **Role Hierarchy** — roles that inherit permissions from parent roles
2. **Attribute-Based Access Control (ABAC)** — conditions like time, IP, department
3. **Delegation** — users grant temporary access to others
4. **Audit Trail** — track all permission checks + grants/revokes
5. **Role Analytics** — report on permission usage patterns

---

## Testing Roles

```go
// Test that admin has permission
adminRole := domain.SystemRoles["admin"]
if !adminRole.CanAdmin(domain.ResourceMembership) {
  t.Errorf("admin should have admin permission")
}

// Test that viewer doesn't have write
viewerRole := domain.SystemRoles["viewer"]
if viewerRole.CanWrite(domain.ResourceTenant) {
  t.Errorf("viewer should not have write permission")
}

// Test custom role creation
customRole, _ := rbacSvc.CreateTenantRole(
  ctx, tenantID, "analyst",
  "Data analyst", perms,
)
if !rbacSvc.CanRead(customRole, domain.ResourceAudit) {
  t.Errorf("analyst should have read permission on audit")
}
```

---

## API Endpoints (Future — R0.3+)

```
GET  /roles                                 # List system + tenant roles
POST /roles                                 # Create custom role
GET  /roles/{roleID}                        # Get role details
PUT  /roles/{roleID}                        # Update custom role
DELETE /roles/{roleID}                      # Delete custom role

POST /roles/{roleID}/permissions            # Grant permission
DELETE /roles/{roleID}/permissions/{permID} # Revoke permission

GET  /memberships/{userID}/role             # Get user's role
PUT  /memberships/{userID}/role             # Change user's role
```

---

## Related Documentation

- **API:** [`docs/API.md`](./API.md)
- **Architecture:** [`docs/architecture/`](./architecture/)
- **Tenancy:** Code in `internal/tenancy/`
