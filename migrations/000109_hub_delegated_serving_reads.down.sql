DROP POLICY IF EXISTS message_media_analysis_read_delegated ON message_media_analysis;
DROP POLICY IF EXISTS message_outbound_media_read_delegated ON message_outbound_media;
DROP POLICY IF EXISTS message_media_read_delegated ON message_media;
DROP POLICY IF EXISTS contacts_read_delegated ON contacts;
DROP FUNCTION IF EXISTS delegated_tenants(TEXT, TEXT);
-- the 000108 definitions (before acting_hub())
CREATE OR REPLACE FUNCTION actor_has_permission(p_tenant_id UUID, p_user_id UUID, p_permission TEXT) RETURNS BOOLEAN AS $$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND CASE
           WHEN NULLIF(current_setting('app.acting_hub', true), '') IS NULL THEN
             EXISTS (SELECT 1 FROM public.memberships m JOIN public.role_permissions rp ON rp.role_id = m.role_id
                     WHERE m.tenant_id = p_tenant_id AND m.user_id = p_user_id AND m.status = 'active' AND rp.permission_key = p_permission)
           ELSE p_permission = ANY (public.delegated_permissions(p_tenant_id, p_user_id, NULLIF(current_setting('app.acting_hub', true), '')::UUID))
         END;
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

CREATE OR REPLACE FUNCTION has_delegated_access(p_tenant_id UUID, p_user_id UUID, p_domain TEXT, p_need TEXT) RETURNS BOOLEAN AS $$
  SELECT NULLIF(current_setting('app.acting_hub', true), '') IS NOT NULL
     AND EXISTS (
       SELECT 1
       FROM unnest(public.delegated_permissions(p_tenant_id, p_user_id, NULLIF(current_setting('app.acting_hub', true), '')::UUID)) k
       JOIN public.permission_domains d ON d.permission_key = k
       WHERE d.domain = p_domain AND (d.need = 'write' OR p_need = 'read'));
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

DROP POLICY IF EXISTS audit_events_hub_serve_read_own ON audit_events;
CREATE POLICY audit_events_hub_serve_read_own ON audit_events FOR SELECT
  USING (tenant_id IS NOT NULL AND actor_id = current_user_id()
         AND cardinality(delegated_permissions(tenant_id, current_user_id(), NULLIF(current_setting('app.acting_hub', true), '')::UUID)) > 0);
DROP FUNCTION IF EXISTS acting_hub();
-- the member predicates go back to the 000097 bodies
CREATE OR REPLACE FUNCTION has_active_membership(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND EXISTS (
       SELECT 1 FROM public.memberships
       WHERE tenant_id = p_tenant_id AND user_id = p_user_id AND status = 'active'
     );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

CREATE OR REPLACE FUNCTION has_active_admin_membership(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND EXISTS (
       SELECT 1 FROM public.memberships m
       JOIN public.roles r ON m.role_id = r.id
       WHERE m.tenant_id = p_tenant_id AND m.user_id = p_user_id
         AND m.status = 'active' AND r.key = 'tenant_admin'
     );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
