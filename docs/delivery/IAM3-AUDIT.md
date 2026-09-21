# IAM3 — Mini Audit

**Data:** 2026-09-21  
**Estado:** Auditando antes de implementação  
**Security classification:** FRONTIER_LLM obrigatório

---

## 1. EXISTING: Schema + Logic

### Migrations

- **000002_memberships_rbac.up/down.sql**
  - Tables: `roles`, `permissions`, `role_permissions`, `memberships.role_id`
  - System roles inserted: `system_admin`, `tenant_admin`, `tenant_supervisor`, `tenant_agent`, `hub_admin`
  - Permissions: `tenant.read`, `tenant.manage`, `membership.read`, `membership.manage`, `audit.read`, `hub.read`, `hub.manage`, `grant.read`, `grant.manage`
  - Role-permission joins configured

- **000006_app_role.up/down.sql** — (not inspected in detail, exists)
- **000023_conversation_permissions.up/down.sql** — (exists)
- **Most recent:** 000035_membership_invitations (does not alter role/permission schema)

### Domain Models

**File:** `internal/rbac/domain/role.go`

```go
type PermissionAction string  // read, write, admin, delete
type PermissionResource string // tenant, membership, audit, settings
type Permission struct { ... }
type Role struct {
  ID, TenantID, Name, Description, IsSystem
  Permissions []*Permission
}

SystemRoles map = ["superadmin", "admin", "viewer", "editor"] // codes, not the migration names
```

**Divergence detected:** Domain has `admin`, `editor`, `viewer`, `guest`, `superadmin`. Migration 000002 has `tenant_admin`, `tenant_supervisor`, `tenant_agent`, `hub_admin`, `system_admin`. **Git/migrations win — but code doesn't load them correctly yet.**

### Application Service

**File:** `internal/rbac/application/service.go`

Methods implemented:
- `GetRole(ctx, roleID UUID)` → *Role
- `GetSystemRole(ctx, roleName string)` → *Role
- `GetTenantRole(ctx, tenantID UUID, roleName string)` → *Role
- `ListTenantRoles(ctx, tenantID UUID)` → []*Role
- `CreateTenantRole(ctx, tenantID UUID, roleName string, description string, permissions []*Permission)` → *Role
- `UpdateRolePermissions(ctx, roleID UUID, permissions []*Permission)` → *Role
- `DeleteTenantRole(ctx, roleID UUID)` → error
- `CheckPermission(ctx, role *Role, resource, action)` → bool
- `CanRead(role, resource)` → bool
- `CanWrite(role, resource)` → bool
- `CanAdmin(role, resource)` → bool

### Ports/Interfaces

**File:** `internal/rbac/ports/role_repository.go` (expected but not verified) — would define I/O contracts.

### Documentation

**File:** `docs/RBAC.md` — comprehensive (5 system roles, resources, actions, permission matrix, examples, testing guide, future endpoints).

---

## 2. MISSING: HTTP Endpoints + Enforcement

### Endpoints Not Exposed

Per `docs/RBAC.md` "API Endpoints (Future — R0.3+)":

```
GET  /api/v1/tenants/{tenant_id}/roles              # List
POST /api/v1/tenants/{tenant_id}/roles              # Create
GET  /api/v1/tenants/{tenant_id}/roles/{role_id}    # Get
PUT  /api/v1/tenants/{tenant_id}/roles/{role_id}    # Update
DELETE /api/v1/tenants/{tenant_id}/roles/{role_id}  # Delete
```

Currently: **No HTTP handlers exist for roles CRUD.**

### Permission Enforcement Gaps

Audit of team/memberships endpoints:

- `internal/tenancy/adapters/team_http.go`
  - `GetTeamMembers()` — no explicit role permission check (relies on RLS?)
  - `UpdateMembership()` — checks `HasPermission("membership.manage")` ✓
  - `ListRoles()` — exists, returns role options
  - `MyAccess()` — returns user's role + permissions

**Permission checks exist in some places but incomplete. Audit needed for all sensitive operations.**

### HTTP Server Registration

**File:** `internal/platform/httpserver/server.go`

- Registers tenancy handlers: `RegisterTenancyHandlers()`
- Registers invitation handlers: `RegisterInvitationHandlers()`
- **No RBAC handler registration.**

---

## 3. Audit Infrastructure

**File:** `internal/audit/` exists

- `AuditEventRepository` interface
- Postgres adapter
- Event recording mechanisms

**Status:** Available for use, but role permission checks not logged consistently.

---

## 4. Tenant Isolation + RLS

- RLS tables (membership_invitations, user_identities, users) have RLS policies
- **roles table:** Not found in RLS completeness tests yet — needs audit
- Tenant boundary: Must verify in role create/update/delete

---

## 5. Documentation Divergence

| Aspect | docs/RBAC.md | Migration 000002 | Domain code |
|--------|--------------|------------------|-------------|
| System roles | superadmin, admin, editor, viewer, guest | system_admin, tenant_admin, tenant_supervisor, tenant_agent, hub_admin | admin, editor, viewer, guest, superadmin |
| Resources | tenant, membership, audit, settings | — (implicit in permission keys) | tenant, membership, audit, settings |
| Actions | read, write, admin, delete | (implicit in permission keys like tenant.read) | read, write, admin, delete |
| Permission format | conceptual | `{resource}.{action}` e.g. `tenant.read` | separate Resource + Action enums |

**Decision:** Use migration schema (Git source of truth) but align domain code to load correct system roles from DB.

---

## 6. Test Coverage

- Domain tests exist (role.go)
- Application service tests exist (service_test.go) — `TestUpdateRolePermissions`, etc.
- **HTTP endpoint tests: None** (because no endpoints yet)
- **Integration tests (Postgres real): Not verified**
- **Privilege escalation tests: Not verified**
- **Tenant A/B isolation tests: Not verified**
- **RLS completeness for roles: Not verified**

---

## 7. Security Checks

- ✓ System role immutability — domain logic exists
- ✓ Tenant-scoped custom roles — schema supports
- ✓ Permission model — resources + actions defined
- ❌ Privilege escalation prevention — not tested
- ❌ Tenant boundary enforcement — not tested
- ❌ RLS on roles table — not verified
- ❌ Runtime role (omnira_app) permissions — not verified

---

## 8. Immediate Gaps (IAM3 Scope)

1. **Divergent system roles** — domain code must load from migration (tenant_admin, not admin)
   - Recommendation: Fix domain system roles to match DB, or update migration to match docs

2. **HTTP CRUD endpoints** — fully missing

3. **Permission enforcement** — incomplete in handlers

4. **Privilege escalation tests** — critical for security

5. **Tenant A/B isolation tests** — critical for multi-tenancy

6. **RLS completeness** — roles table needs RLS policies if not present

7. **Audit logging** — permission checks should be logged consistently

---

## 9. Implementation Plan (IAM3 Scope)

### IAM3.1 — Align Schema + Domain

- **Action:** Decide: update domain to match migration, or update migration to match docs?
- **Recommendation:** Keep migration authority (Git); fix domain load logic
- **Commits:** 1 (fix domain role initialization)

### IAM3.2 — HTTP CRUD Endpoints

- **Action:** Implement `internal/rbac/adapters/role_http.go` with:
  - `ListTenantRoles(w, r)` — GET /roles
  - `GetRole(w, r)` — GET /roles/{role_id}
  - `CreateRole(w, r)` — POST /roles
  - `UpdateRole(w, r)` — PATCH /roles/{role_id}
  - `DeleteRole(w, r)` — DELETE /roles/{role_id}
- **Register:** In `internal/platform/httpserver/server.go` → `RegisterRBACHandlers()`
- **Commits:** 1-2

### IAM3.3 — Permission Enforcement + Adversarial Tests

- **Action:** Audit all sensitive operations for permission checks
- **Add:** Missing `CheckPermission()` calls where operations lack authorization
- **Test:** Privilege escalation, system role immutability, tenant boundary
- **Commits:** 1-2

### IAM3.4 — RLS + Integration + Audit

- **Action:** Verify RLS policies on roles table
- **Add:** Audit logging for role CRUD + permission checks
- **Test:** Fresh DB migrations, RLS completeness, real Postgres
- **Commits:** 1

### IAM3.5 — OpenAPI + Final Gate

- **Action:** Document endpoints in OpenAPI spec
- **Commits:** 1 (docs)

---

## 10. Security Gate (FRONTIER_LLM)

This task modifies authorization/RBAC — **security-critical domain.**

Router decision: `authorization, tenancy, security` → FRONTIER_LLM (mandatory, no downgrade).

Shadow log: decision + outcome (after full gate suite PASS).

---

## Next: Proceed to Implementation

Ready for IAM3.1 → IAM3.5 slices.
