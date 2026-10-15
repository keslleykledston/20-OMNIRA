-- ADR-0049 addendum: the protocol prefix belongs to the company (VLS-1001, KTG-1001), is unique among companies, is changed only by whoever manages the company
-- and is frozen as soon as the first protocol was issued (a customer may have written the number down).
--
--  * A counter row with last_value = 0 is a RESERVED prefix: the company chose it and no protocol exists yet. The first protocol turns it into `start`.
--  * The index is global on purpose: the prefix is what tells a Hub agent, who attends several companies, which company a number belongs to.
--    A uniqueness error reveals only that the three letters are taken, never by whom.
--  * Only the session counter is affected; the case counter keeps its fixed OC prefix.
SET LOCAL lock_timeout = '5s';

-- Pre-flight: before this migration the application used one default prefix for everybody ("AT"). Migrations 000150-000155 were written together and never
-- reached a database that holds real counters, but if two companies DO share a prefix (a database where 000150-000154 already ran and issued protocols), the unique
-- index below cannot be built; say so plainly instead of failing with an opaque error. Protocols already issued are never renamed: resolve it by hand first.
DO $$
DECLARE dup TEXT;
BEGIN
  SELECT string_agg(prefix || ' (' || n || ' companies)', ', ') INTO dup
    FROM (SELECT prefix, count(*) AS n FROM service_counters WHERE kind = 'session' GROUP BY prefix HAVING count(*) > 1) d;
  IF dup IS NOT NULL THEN
    RAISE EXCEPTION 'service_counters: session prefixes shared by several companies: %. Give each company its own prefix before applying 000155 (issued protocols keep their text).', dup
      USING ERRCODE = 'unique_violation';
  END IF;
END $$;

CREATE UNIQUE INDEX service_counters_session_prefix_uq ON service_counters (prefix) WHERE kind = 'session';

CREATE OR REPLACE FUNCTION service_counters_before_update() RETURNS trigger AS $$
BEGIN
  IF NEW.prefix IS DISTINCT FROM OLD.prefix THEN
    IF OLD.last_value > 0 THEN
      RAISE EXCEPTION 'the protocol prefix is frozen once a protocol was issued' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT (public.is_system_admin() OR public.actor_has_permission(OLD.tenant_id, public.current_user_id(), 'tenant.manage')) THEN
      RAISE EXCEPTION 'changing the protocol prefix needs tenant.manage' USING ERRCODE = 'insufficient_privilege';
    END IF;
  END IF;
  -- a counter moves forward by one; a reserved prefix (0) takes its first number
  IF NEW.kind IS DISTINCT FROM OLD.kind OR NOT (NEW.last_value = OLD.last_value OR NEW.last_value = OLD.last_value + 1 OR (OLD.last_value = 0 AND NEW.last_value > 0)) THEN
    RAISE EXCEPTION 'a protocol counter moves forward by one' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;
CREATE TRIGGER service_counters_before_update_trg BEFORE UPDATE ON service_counters FOR EACH ROW EXECUTE FUNCTION service_counters_before_update();

-- A new counter row starts at its first number (> 0, derived by the application when the company chose nothing); a RESERVED prefix (0) is the company's
-- configuration and needs tenant.manage.
DROP POLICY service_counters_insert_tenant ON service_counters;
CREATE POLICY service_counters_insert_tenant ON service_counters FOR INSERT WITH CHECK (
  is_system_admin()
  OR (last_value > 0 AND actor_has_permission(tenant_id, current_user_id(), 'service.create'))
  OR actor_has_permission(tenant_id, current_user_id(), 'tenant.manage'));

-- Whoever manages the company can change its RESERVED prefix without also holding service.create (the trigger above is what asks for tenant.manage).
DROP POLICY service_counters_update_tenant ON service_counters;
CREATE POLICY service_counters_update_tenant ON service_counters FOR UPDATE
  USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.create') OR actor_has_permission(tenant_id, current_user_id(), 'tenant.manage'))
  WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.create') OR actor_has_permission(tenant_id, current_user_id(), 'tenant.manage'));

-- ... and read it: the INSERT ... RETURNING and the ON CONFLICT DO UPDATE of the reservation need the row to be visible to its author.
DROP POLICY service_counters_read_tenant ON service_counters;
CREATE POLICY service_counters_read_tenant ON service_counters FOR SELECT
  USING (is_system_admin() OR tenant_id IN (SELECT service_read_tenants()) OR actor_has_permission(tenant_id, current_user_id(), 'tenant.manage'));
