-- Rolls back addendum 2: counters are written directly again (the state 000155 left). Safe: nothing issued is touched.
SET LOCAL lock_timeout = '5s';
DROP POLICY IF EXISTS service_counters_update_tenant ON service_counters;
CREATE POLICY service_counters_update_tenant ON service_counters FOR UPDATE
  USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.create') OR actor_has_permission(tenant_id, current_user_id(), 'tenant.manage'))
  WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.create') OR actor_has_permission(tenant_id, current_user_id(), 'tenant.manage'));
DROP POLICY IF EXISTS service_counters_insert_tenant ON service_counters;
CREATE POLICY service_counters_insert_tenant ON service_counters FOR INSERT WITH CHECK (
  is_system_admin()
  OR (last_value > 0 AND actor_has_permission(tenant_id, current_user_id(), 'service.create'))
  OR actor_has_permission(tenant_id, current_user_id(), 'tenant.manage'));
DROP TRIGGER IF EXISTS service_counters_guard_trg ON service_counters;
DROP FUNCTION IF EXISTS service_counters_guard();
CREATE FUNCTION service_counters_before_update() RETURNS trigger AS $$
BEGIN
  IF NEW.prefix IS DISTINCT FROM OLD.prefix THEN
    IF OLD.last_value > 0 THEN
      RAISE EXCEPTION 'the protocol prefix is frozen once a protocol was issued' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT (public.is_system_admin() OR public.actor_has_permission(OLD.tenant_id, public.current_user_id(), 'tenant.manage')) THEN
      RAISE EXCEPTION 'changing the protocol prefix needs tenant.manage' USING ERRCODE = 'insufficient_privilege';
    END IF;
  END IF;
  IF NEW.kind IS DISTINCT FROM OLD.kind OR NOT (NEW.last_value = OLD.last_value OR NEW.last_value = OLD.last_value + 1 OR (OLD.last_value = 0 AND NEW.last_value > 0)) THEN
    RAISE EXCEPTION 'a protocol counter moves forward by one' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;
CREATE TRIGGER service_counters_before_update_trg BEFORE UPDATE ON service_counters FOR EACH ROW EXECUTE FUNCTION service_counters_before_update();
DROP FUNCTION IF EXISTS service_suggest_prefix(UUID);
DROP FUNCTION IF EXISTS service_issue_protocol(UUID, TEXT);
DROP FUNCTION IF EXISTS service_prefix_candidates(TEXT);
ALTER TABLE service_counters DROP CONSTRAINT IF EXISTS service_counters_case_prefix_chk;
