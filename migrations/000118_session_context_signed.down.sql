-- Rollback of 000118: back to the GUC-trusting definitions (generated from the live definitions of 000117).
CREATE OR REPLACE FUNCTION public.acting_hub()
 RETURNS uuid
 LANGUAGE sql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
  SELECT CASE WHEN v ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN v::UUID END
  FROM (SELECT NULLIF(current_setting('app.acting_hub', true), '') AS v) s;
$function$
;
CREATE OR REPLACE FUNCTION public.acting_tenant()
 RETURNS uuid
 LANGUAGE sql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
  SELECT CASE WHEN v ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN v::UUID END
  FROM (SELECT NULLIF(current_setting('app.acting_tenant', true), '') AS v) s;
$function$
;
CREATE OR REPLACE FUNCTION public.actor_has_permission(p_tenant_id uuid, p_user_id uuid, p_permission text)
 RETURNS boolean
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND CASE
           WHEN NULLIF(current_setting('app.acting_hub', true), '') IS NULL THEN
             EXISTS (SELECT 1 FROM public.memberships m JOIN public.role_permissions rp ON rp.role_id = m.role_id
                     WHERE m.tenant_id = p_tenant_id AND m.user_id = p_user_id AND m.status = 'active' AND rp.permission_key = p_permission)
           ELSE COALESCE(p_permission = ANY (public.delegated_permissions(p_tenant_id, p_user_id, public.acting_hub())), false)
         END;
$function$
;
CREATE OR REPLACE FUNCTION public.current_user_id()
 RETURNS uuid
 LANGUAGE sql
 STABLE
AS $function$
  SELECT NULLIF(current_setting('app.current_user_id', TRUE), '')::UUID;
$function$
;
CREATE OR REPLACE FUNCTION public.has_active_admin_membership(p_tenant_id uuid, p_user_id uuid)
 RETURNS boolean
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND NULLIF(current_setting('app.acting_hub', true), '') IS NULL
     AND EXISTS (
       SELECT 1 FROM public.memberships m
       JOIN public.roles r ON m.role_id = r.id
       WHERE m.tenant_id = p_tenant_id AND m.user_id = p_user_id
         AND m.status = 'active' AND r.key = 'tenant_admin'
     );
$function$
;
CREATE OR REPLACE FUNCTION public.has_active_membership(p_tenant_id uuid, p_user_id uuid)
 RETURNS boolean
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND NULLIF(current_setting('app.acting_hub', true), '') IS NULL
     AND EXISTS (
       SELECT 1 FROM public.memberships
       WHERE tenant_id = p_tenant_id AND user_id = p_user_id AND status = 'active'
     );
$function$
;
CREATE OR REPLACE FUNCTION public.is_system_admin()
 RETURNS boolean
 LANGUAGE sql
 STABLE
AS $function$
  SELECT COALESCE(NULLIF(current_setting('app.is_system_admin', TRUE), '')::BOOLEAN, FALSE);
$function$
;
CREATE OR REPLACE FUNCTION public.lock_served_tenant(p_tenant_id uuid, p_user_id uuid, p_hub_id uuid)
 RETURNS boolean
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
BEGIN
  IF NOT (p_user_id = public.current_user_id() OR public.is_system_admin()) THEN
    RETURN false;
  END IF;
  IF cardinality(public.delegated_permissions(p_tenant_id, p_user_id, p_hub_id)) = 0 THEN
    RETURN false;
  END IF;
  PERFORM 1 FROM public.tenants WHERE id = p_tenant_id FOR SHARE;
  PERFORM 1 FROM public.service_hubs WHERE id = p_hub_id FOR SHARE;
  PERFORM 1 FROM public.hub_tenant_service_contracts WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id FOR SHARE;
  PERFORM 1 FROM public.hub_memberships WHERE hub_id = p_hub_id AND user_id = p_user_id FOR SHARE;
  PERFORM 1 FROM public.users WHERE id = p_user_id FOR SHARE;
  PERFORM 1 FROM public.effective_access_grants WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id AND user_id = p_user_id FOR SHARE;
  IF cardinality(public.delegated_permissions(p_tenant_id, p_user_id, p_hub_id)) = 0 THEN
    RETURN false;
  END IF;
  PERFORM set_config('app.acting_hub', p_hub_id::TEXT, true);
  PERFORM set_config('app.acting_tenant', p_tenant_id::TEXT, true);
  -- the policies of the delegated context are costly on paper (nested function calls), which makes PostgreSQL's cost model switch JIT compilation on for an inbox
  -- scan; compiling ~250 functions took 1.2 s per statement, longer than the query itself (measured). Off for this transaction only.
  PERFORM set_config('jit', 'off', true);
  RETURN true;
END;
$function$
;
ALTER POLICY conversations_read_tenant  ON conversations  USING ((NULLIF(current_setting('app.acting_hub', true), '') IS NULL AND has_active_membership(tenant_id, current_user_id())) OR is_system_admin());
ALTER POLICY messages_read_tenant       ON messages       USING ((NULLIF(current_setting('app.acting_hub', true), '') IS NULL AND has_active_membership(tenant_id, current_user_id())) OR is_system_admin());
ALTER POLICY contacts_read_tenant       ON contacts       USING ((NULLIF(current_setting('app.acting_hub', true), '') IS NULL AND has_active_membership(tenant_id, current_user_id())) OR is_system_admin());
ALTER POLICY tickets_read_tenant        ON tickets        USING ((NULLIF(current_setting('app.acting_hub', true), '') IS NULL AND has_active_membership(tenant_id, current_user_id())) OR is_system_admin());
ALTER POLICY message_media_read_tenant  ON message_media  USING ((NULLIF(current_setting('app.acting_hub', true), '') IS NULL AND has_active_membership(tenant_id, current_user_id())) OR is_system_admin());
ALTER POLICY contact_notes_read_tenant  ON contact_notes  USING ((NULLIF(current_setting('app.acting_hub', true), '') IS NULL AND has_active_membership(tenant_id, current_user_id())) OR is_system_admin());

DROP FUNCTION IF EXISTS ctx_set_mode(TEXT);
DROP FUNCTION IF EXISTS ctx_refused();
DROP FUNCTION IF EXISTS ctx_challenge();
DROP FUNCTION IF EXISTS ctx_install_readers(TEXT);
DROP FUNCTION IF EXISTS ctx_verified(TEXT);
DROP FUNCTION IF EXISTS ctx_drop_previous_key();
DROP FUNCTION IF EXISTS ctx_install_key(BYTEA);
DROP FUNCTION IF EXISTS ctx_acting_present();
DROP FUNCTION IF EXISTS ctx_measure(TEXT, TEXT, TEXT);
DROP FUNCTION IF EXISTS ctx_set_acting(UUID, UUID);
DROP FUNCTION IF EXISTS ctx_begin(TEXT);
DROP FUNCTION IF EXISTS ctx_signed(TEXT, TEXT, INTEGER);
DROP FUNCTION IF EXISTS ctx_mode();
DROP TABLE IF EXISTS session_ctx_verified;
DROP TABLE IF EXISTS session_ctx_mode;
DROP TABLE IF EXISTS session_ctx_keys;
-- the extension was created by the up migration (nothing else in this schema uses it)
DROP EXTENSION IF EXISTS pgcrypto;
