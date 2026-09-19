-- Memberships and RBAC tables

-- Roles table (system + tenant-scoped)
CREATE TABLE roles (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  name TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(tenant_id, key)
);

-- System roles (tenant_id IS NULL)
INSERT INTO roles (tenant_id, key, name) VALUES
  (NULL, 'system_admin', 'System Administrator'),
  (NULL, 'tenant_admin', 'Tenant Administrator'),
  (NULL, 'tenant_supervisor', 'Tenant Supervisor'),
  (NULL, 'tenant_agent', 'Tenant Agent'),
  (NULL, 'hub_admin', 'Hub Administrator');

CREATE INDEX idx_roles_tenant_id ON roles(tenant_id);
CREATE INDEX idx_roles_key ON roles(key);

-- Permissions table
CREATE TABLE permissions (
  key TEXT PRIMARY KEY,
  description TEXT
);

-- Core permissions
INSERT INTO permissions (key, description) VALUES
  ('tenant.read', 'Read tenant information'),
  ('tenant.manage', 'Manage tenant'),
  ('membership.read', 'Read memberships'),
  ('membership.manage', 'Manage memberships'),
  ('audit.read', 'Read audit logs'),
  ('hub.read', 'Read hub information'),
  ('hub.manage', 'Manage hub'),
  ('grant.read', 'Read hub-tenant grants'),
  ('grant.manage', 'Manage hub-tenant grants');

-- Role-permission mapping
CREATE TABLE role_permissions (
  role_id UUID REFERENCES roles(id) ON DELETE CASCADE,
  permission_key TEXT REFERENCES permissions(key) ON DELETE CASCADE,
  PRIMARY KEY (role_id, permission_key)
);

-- Grant default permissions
INSERT INTO role_permissions (role_id, permission_key)
SELECT r.id, p.key FROM roles r, permissions p
WHERE r.key = 'tenant_admin' AND p.key IN ('tenant.read', 'tenant.manage', 'membership.read', 'membership.manage', 'audit.read');

INSERT INTO role_permissions (role_id, permission_key)
SELECT r.id, p.key FROM roles r, permissions p
WHERE r.key = 'tenant_supervisor' AND p.key IN ('tenant.read', 'membership.read', 'audit.read');

INSERT INTO role_permissions (role_id, permission_key)
SELECT r.id, p.key FROM roles r, permissions p
WHERE r.key = 'tenant_agent' AND p.key IN ('tenant.read');

-- Memberships table
CREATE TABLE memberships (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role_id UUID NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'revoked')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(tenant_id, user_id)
);

CREATE INDEX idx_memberships_tenant_id ON memberships(tenant_id);
CREATE INDEX idx_memberships_user_id ON memberships(user_id);
CREATE INDEX idx_memberships_role_id ON memberships(role_id);
CREATE INDEX idx_memberships_status ON memberships(status);

-- Enable RLS on RBAC tables
ALTER TABLE roles ENABLE ROW LEVEL SECURITY;
ALTER TABLE permissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE role_permissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE memberships ENABLE ROW LEVEL SECURITY;
