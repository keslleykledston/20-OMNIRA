-- Rollback: RLS policies

DROP FUNCTION IF EXISTS is_system_admin() CASCADE;
DROP FUNCTION IF EXISTS current_user_id() CASCADE;

DROP POLICY IF EXISTS role_permissions_read_public ON role_permissions;
DROP POLICY IF EXISTS roles_read_public ON roles;
DROP POLICY IF EXISTS permissions_read_public ON permissions;

DROP POLICY IF EXISTS outbox_events_insert_authenticated ON outbox_events;
DROP POLICY IF EXISTS outbox_events_read_tenant ON outbox_events;

DROP POLICY IF EXISTS audit_events_insert_authenticated ON audit_events;
DROP POLICY IF EXISTS audit_events_read_tenant ON audit_events;

DROP POLICY IF EXISTS memberships_update_admin ON memberships;
DROP POLICY IF EXISTS memberships_manage_tenant ON memberships;
DROP POLICY IF EXISTS memberships_read_tenant ON memberships;

DROP POLICY IF EXISTS tenants_update_admin ON tenants;
DROP POLICY IF EXISTS tenants_insert_admin ON tenants;
DROP POLICY IF EXISTS tenants_read_members ON tenants;

DROP POLICY IF EXISTS users_update_self ON users;
DROP POLICY IF EXISTS users_read_self ON users;
