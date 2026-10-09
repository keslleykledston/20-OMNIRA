DROP POLICY IF EXISTS message_media_analysis_read_delegated ON message_media_analysis;
DROP POLICY IF EXISTS message_outbound_media_read_delegated ON message_outbound_media;
DROP POLICY IF EXISTS message_media_read_delegated ON message_media;
DROP POLICY IF EXISTS contacts_read_delegated ON contacts;
DROP FUNCTION IF EXISTS delegated_tenants(TEXT, TEXT);
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
