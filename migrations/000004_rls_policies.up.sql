-- Row-level security policies

-- Users: only system admins and self can access
CREATE POLICY users_read_self ON users FOR SELECT
  USING (current_user_id() = id OR is_system_admin());

CREATE POLICY users_update_self ON users FOR UPDATE
  USING (current_user_id() = id OR is_system_admin())
  WITH CHECK (current_user_id() = id OR is_system_admin());

-- Tenants: accessible to members and system admins
CREATE POLICY tenants_read_members ON tenants FOR SELECT
  USING (
    EXISTS (
      SELECT 1 FROM memberships m
      WHERE m.tenant_id = tenants.id
      AND m.user_id = current_user_id()
      AND m.status = 'active'
    ) OR is_system_admin()
  );

CREATE POLICY tenants_update_admin ON tenants FOR UPDATE
  USING (
    EXISTS (
      SELECT 1 FROM memberships m
      JOIN roles r ON m.role_id = r.id
      WHERE m.tenant_id = tenants.id
      AND m.user_id = current_user_id()
      AND m.status = 'active'
      AND r.key = 'tenant_admin'
    ) OR is_system_admin()
  )
  WITH CHECK (
    EXISTS (
      SELECT 1 FROM memberships m
      JOIN roles r ON m.role_id = r.id
      WHERE m.tenant_id = tenants.id
      AND m.user_id = current_user_id()
      AND m.status = 'active'
      AND r.key = 'tenant_admin'
    ) OR is_system_admin()
  );

-- Memberships: accessible within tenant or by system admin
CREATE POLICY memberships_read_tenant ON memberships FOR SELECT
  USING (
    EXISTS (
      SELECT 1 FROM memberships m2
      WHERE m2.tenant_id = memberships.tenant_id
      AND m2.user_id = current_user_id()
      AND m2.status = 'active'
    ) OR is_system_admin()
  );

CREATE POLICY memberships_manage_tenant ON memberships FOR INSERT
  WITH CHECK (
    EXISTS (
      SELECT 1 FROM memberships m2
      JOIN roles r ON m2.role_id = r.id
      WHERE m2.tenant_id = memberships.tenant_id
      AND m2.user_id = current_user_id()
      AND m2.status = 'active'
      AND r.key = 'tenant_admin'
    ) OR is_system_admin()
  );

CREATE POLICY memberships_update_admin ON memberships FOR UPDATE
  USING (
    EXISTS (
      SELECT 1 FROM memberships m2
      JOIN roles r ON m2.role_id = r.id
      WHERE m2.tenant_id = memberships.tenant_id
      AND m2.user_id = current_user_id()
      AND m2.status = 'active'
      AND r.key = 'tenant_admin'
    ) OR is_system_admin()
  )
  WITH CHECK (
    EXISTS (
      SELECT 1 FROM memberships m2
      JOIN roles r ON m2.role_id = r.id
      WHERE m2.tenant_id = memberships.tenant_id
      AND m2.user_id = current_user_id()
      AND m2.status = 'active'
      AND r.key = 'tenant_admin'
    ) OR is_system_admin()
  );

-- Audit events: accessible within tenant or by system admin
CREATE POLICY audit_events_read_tenant ON audit_events FOR SELECT
  USING (
    (tenant_id IS NULL AND is_system_admin()) OR
    EXISTS (
      SELECT 1 FROM memberships m
      WHERE m.tenant_id = audit_events.tenant_id
      AND m.user_id = current_user_id()
      AND m.status = 'active'
    ) OR is_system_admin()
  );

-- Outbox events: accessible within tenant
CREATE POLICY outbox_events_read_tenant ON outbox_events FOR SELECT
  USING (
    EXISTS (
      SELECT 1 FROM memberships m
      WHERE m.tenant_id = outbox_events.tenant_id
      AND m.user_id = current_user_id()
      AND m.status = 'active'
    ) OR is_system_admin()
  );

-- Permissions and Roles: readable by all authenticated users
CREATE POLICY permissions_read_public ON permissions FOR SELECT
  USING (TRUE);

CREATE POLICY roles_read_public ON roles FOR SELECT
  USING (TRUE);

CREATE POLICY role_permissions_read_public ON role_permissions FOR SELECT
  USING (TRUE);

-- Helper functions for RLS
CREATE OR REPLACE FUNCTION current_user_id() RETURNS UUID AS $$
  SELECT current_setting('app.current_user_id', TRUE)::UUID;
$$ LANGUAGE SQL STABLE;

CREATE OR REPLACE FUNCTION is_system_admin() RETURNS BOOLEAN AS $$
  SELECT current_setting('app.is_system_admin', TRUE)::BOOLEAN;
$$ LANGUAGE SQL STABLE;
