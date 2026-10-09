-- An account that is not active must not read or answer through a Hub either (Codex review of ADR-0038 phases 3-4). has_active_hub_access is
-- the single definition behind every delegated read policy, the inbox projection read and the write-time re-check (ADR-0037); adding the
-- condition here closes all of them at once. Same function as 000098 plus user_is_active (000103). Grants, contracts and rows are untouched:
-- reactivating the account restores access without re-provisioning.
CREATE OR REPLACE FUNCTION has_active_hub_access(
  p_user_id UUID, p_tenant_id UUID, p_queue_id UUID DEFAULT NULL, p_hub_id UUID DEFAULT NULL, p_check_scope BOOLEAN DEFAULT true,
  p_require_reply BOOLEAN DEFAULT false
) RETURNS BOOLEAN AS $$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND public.user_is_active(p_user_id)
     AND EXISTS (
    SELECT 1
    FROM public.effective_access_grants g
    JOIN public.hub_tenant_service_contracts c ON c.id = g.service_contract_id
    JOIN public.service_hubs h ON h.id = g.hub_id
    JOIN public.hub_memberships hm ON hm.hub_id = g.hub_id AND hm.user_id = g.user_id
    JOIN public.tenants t ON t.id = g.tenant_id
    WHERE g.user_id = p_user_id
      AND g.tenant_id = p_tenant_id
      AND (p_hub_id IS NULL OR g.hub_id = p_hub_id)
      AND t.status = 'active'
      AND h.status = 'active'
      AND g.status = 'active' AND g.valid_from <= now() AND (g.valid_until IS NULL OR g.valid_until > now())
      AND (NOT p_require_reply OR g.can_reply)
      AND c.status = 'active' AND c.valid_from <= now() AND (c.valid_until IS NULL OR c.valid_until > now())
      AND CASE
            WHEN jsonb_typeof(c.service_scope) IS DISTINCT FROM 'object' THEN false
            WHEN NOT p_check_scope THEN true
            WHEN c.service_scope -> 'queue_ids' IS NULL THEN true
            WHEN jsonb_typeof(c.service_scope -> 'queue_ids') <> 'array' THEN false
            ELSE p_queue_id IS NOT NULL AND (c.service_scope -> 'queue_ids') @> to_jsonb(p_queue_id::text)
          END
  );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
