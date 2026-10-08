-- Closes a cross-user oracle in the original tenant RLS helpers (000004), found by the Hub adversarial review:
-- any session that can run SQL could call has_active_membership(<tenant>, <someone else>) and learn whether
-- another user belongs to a tenant (and has_active_admin_membership: whether they administer it).
--
-- The helpers keep their signature and body; they now answer only for the SESSION user, or for a system
-- session (is_system_admin(), used by workers and identity provisioning). Every existing caller already passes
-- current_user_id() (all policies), so behaviour for legitimate callers is unchanged. Relations are
-- schema-qualified in addition to the pinned search_path from 000096.
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
