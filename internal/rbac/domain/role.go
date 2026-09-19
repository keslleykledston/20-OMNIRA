package domain

import (
	"github.com/google/uuid"
)

// PermissionAction — ação que uma permission autoriza
type PermissionAction string

const (
	ActionRead   PermissionAction = "read"
	ActionWrite  PermissionAction = "write"
	ActionAdmin  PermissionAction = "admin"
	ActionDelete PermissionAction = "delete"
)

// PermissionResource — recurso que uma permission protege
type PermissionResource string

const (
	ResourceTenant     PermissionResource = "tenant"
	ResourceMembership PermissionResource = "membership"
	ResourceAudit      PermissionResource = "audit"
	ResourceSettings   PermissionResource = "settings"
)

// Permission — permissão com recurso + ação
type Permission struct {
	ID       uuid.UUID
	Resource PermissionResource
	Action   PermissionAction
	Name     string // human-readable: "read:tenant", "write:membership"
}

// Role — função com conjunto de permissões
type Role struct {
	ID          uuid.UUID
	TenantID    uuid.UUID    // nil for system roles
	Name        string       // "admin", "viewer", "editor"
	Description string
	IsSystem    bool
	Permissions []*Permission
	CreatedAt   string
}

// HasPermission — verifica se role tem permissão específica
func (r *Role) HasPermission(resource PermissionResource, action PermissionAction) bool {
	if r == nil {
		return false
	}

	for _, perm := range r.Permissions {
		if perm.Resource == resource && perm.Action == action {
			return true
		}
		// Admin tem todas as permissões
		if perm.Action == ActionAdmin {
			return true
		}
	}

	return false
}

// CanRead — shorthand para read permission
func (r *Role) CanRead(resource PermissionResource) bool {
	return r.HasPermission(resource, ActionRead)
}

// CanWrite — shorthand para write permission
func (r *Role) CanWrite(resource PermissionResource) bool {
	return r.HasPermission(resource, ActionWrite)
}

// CanAdmin — shorthand para admin permission
func (r *Role) CanAdmin(resource PermissionResource) bool {
	return r.HasPermission(resource, ActionAdmin)
}

// SystemRoles — roles de sistema pré-definidos
var SystemRoles = map[string]*Role{
	"superadmin": {
		Name:     "superadmin",
		IsSystem: true,
		Description: "Super administrator - full access across all tenants",
		Permissions: []*Permission{
			{Resource: ResourceTenant, Action: ActionAdmin},
			{Resource: ResourceMembership, Action: ActionAdmin},
			{Resource: ResourceAudit, Action: ActionAdmin},
			{Resource: ResourceSettings, Action: ActionAdmin},
		},
	},
	"admin": {
		Name:     "admin",
		IsSystem: true,
		Description: "Tenant administrator - full access within tenant",
		Permissions: []*Permission{
			{Resource: ResourceTenant, Action: ActionRead},
			{Resource: ResourceMembership, Action: ActionAdmin},
			{Resource: ResourceAudit, Action: ActionRead},
			{Resource: ResourceSettings, Action: ActionAdmin},
		},
	},
	"editor": {
		Name:     "editor",
		IsSystem: true,
		Description: "Editor - can read and write data",
		Permissions: []*Permission{
			{Resource: ResourceTenant, Action: ActionRead},
			{Resource: ResourceMembership, Action: ActionRead},
			{Resource: ResourceAudit, Action: ActionRead},
		},
	},
	"viewer": {
		Name:     "viewer",
		IsSystem: true,
		Description: "Viewer - read-only access",
		Permissions: []*Permission{
			{Resource: ResourceTenant, Action: ActionRead},
			{Resource: ResourceAudit, Action: ActionRead},
		},
	},
	"guest": {
		Name:     "guest",
		IsSystem: true,
		Description: "Guest - minimal access",
		Permissions: []*Permission{},
	},
}

// NewTenantRole — cria novo role customizado para tenant
func NewTenantRole(tenantID uuid.UUID, name string, description string, permissions []*Permission) *Role {
	return &Role{
		ID:          uuid.New(),
		TenantID:    tenantID,
		Name:        name,
		Description: description,
		IsSystem:    false,
		Permissions: permissions,
		CreatedAt:   "", // set by repository
	}
}
