-- Rolls back the protocol-prefix addendum. Safe: it only removes a guard and an index; counters and protocols already issued are untouched.
SET LOCAL lock_timeout = '5s';
DROP POLICY IF EXISTS service_counters_insert_tenant ON service_counters;
CREATE POLICY service_counters_insert_tenant ON service_counters FOR INSERT
  WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.create'));
DROP POLICY IF EXISTS service_counters_update_tenant ON service_counters;
CREATE POLICY service_counters_update_tenant ON service_counters FOR UPDATE
  USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.create'))
  WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.create'));
DROP POLICY IF EXISTS service_counters_read_tenant ON service_counters;
CREATE POLICY service_counters_read_tenant ON service_counters FOR SELECT USING (is_system_admin() OR tenant_id IN (SELECT service_read_tenants()));
DROP TRIGGER IF EXISTS service_counters_before_update_trg ON service_counters;
DROP FUNCTION IF EXISTS service_counters_before_update();
DROP INDEX IF EXISTS service_counters_session_prefix_uq;
