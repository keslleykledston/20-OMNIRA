-- Rollback: the original bodies from 000004 (with the search_path pinned by 000096, which has its own down).
CREATE OR REPLACE FUNCTION has_active_membership(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT EXISTS (
    SELECT 1 FROM memberships
    WHERE tenant_id = p_tenant_id AND user_id = p_user_id AND status = 'active'
  );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

CREATE OR REPLACE FUNCTION has_active_admin_membership(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT EXISTS (
    SELECT 1 FROM memberships m
    JOIN roles r ON m.role_id = r.id
    WHERE m.tenant_id = p_tenant_id AND m.user_id = p_user_id
    AND m.status = 'active' AND r.key = 'tenant_admin'
  );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
