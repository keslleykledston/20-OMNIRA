-- ADR-0044 phase 5b: more overflow destinations - a named attendant, a sector (its first useful queue) and a Hub that serves the company.
-- The Hub destination never moves a conversation to another tenant: it moves it, INSIDE the same company, to a queue the Hub's contract
-- already covers, where the Hub's operators serve it under their own grants (ADR-0040). Nobody is made a member.
SET LOCAL lock_timeout = '5s';

ALTER TABLE overflow_steps DROP CONSTRAINT IF EXISTS overflow_steps_destination_kind_check;
ALTER TABLE overflow_steps ADD CONSTRAINT overflow_steps_destination_kind_check CHECK (destination_kind IN ('queue', 'oncall', 'agent', 'sector', 'hub'));
-- for a Hub destination: destination_id is the CONTRACT, destination_queue_id the company queue the Hub serves from
ALTER TABLE overflow_steps ADD COLUMN destination_queue_id UUID;
ALTER TABLE overflow_steps ADD CONSTRAINT overflow_steps_hub_queue_chk CHECK ((destination_kind = 'hub') = (destination_queue_id IS NOT NULL));
ALTER TABLE overflow_steps ADD CONSTRAINT overflow_steps_dest_queue_fk FOREIGN KEY (tenant_id, destination_queue_id) REFERENCES queues (tenant_id, id) ON DELETE CASCADE;

-- What the COMPANY may know about its Hubs: which contracts it has, never who the Hub's operators are. Readable by the company's
-- administrators of agents (the people who can configure a chain); the Hub side keeps its own, stricter policies on the table itself.
CREATE OR REPLACE FUNCTION tenant_hub_contracts(p_tenant UUID)
RETURNS TABLE (contract_id UUID, hub_id UUID, hub_name TEXT, status TEXT, valid_from TIMESTAMPTZ, valid_until TIMESTAMPTZ, queue_ids JSONB)
AS $$
  SELECT c.id, c.hub_id, h.name, c.status, c.valid_from, c.valid_until, c.service_scope -> 'queue_ids'
  FROM public.hub_tenant_service_contracts c JOIN public.service_hubs h ON h.id = c.hub_id
  WHERE c.tenant_id = p_tenant
    AND (public.is_system_admin() OR public.actor_has_permission(p_tenant, public.current_user_id(), 'agent.manage'));
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION tenant_hub_contracts(UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION tenant_hub_contracts(UUID) TO omnira_app;

-- Is this Hub destination usable RIGHT NOW? Same conditions as has_active_hub_access (migration 105, scope checked, reply required),
-- but answering only yes/no for "does the contract have at least one live operator who may reply for this queue", so the company learns
-- nothing about the operators. Kept equal to has_active_hub_access by an oracle test over every grant/contract/hub/tenant state.
CREATE OR REPLACE FUNCTION hub_destination_ready(p_tenant UUID, p_contract UUID, p_queue UUID) RETURNS BOOLEAN AS $$
  SELECT (public.is_system_admin() OR public.actor_has_permission(p_tenant, public.current_user_id(), 'agent.manage'))
     AND EXISTS (
    SELECT 1
    FROM public.effective_access_grants g
    JOIN public.hub_tenant_service_contracts c ON c.id = g.service_contract_id
    JOIN public.service_hubs h ON h.id = g.hub_id
    JOIN public.hub_memberships hm ON hm.hub_id = g.hub_id AND hm.user_id = g.user_id
    JOIN public.tenants t ON t.id = g.tenant_id
    WHERE c.id = p_contract AND g.tenant_id = p_tenant AND c.tenant_id = p_tenant
      AND EXISTS (SELECT 1 FROM public.queues qq WHERE qq.tenant_id = p_tenant AND qq.id = p_queue)
      AND public.user_is_active(g.user_id)
      AND t.status = 'active' AND h.status = 'active'
      AND g.status = 'active' AND g.valid_from <= now() AND (g.valid_until IS NULL OR g.valid_until > now())
      AND g.can_reply
      AND c.status = 'active' AND c.valid_from <= now() AND (c.valid_until IS NULL OR c.valid_until > now())
      AND CASE
            WHEN jsonb_typeof(c.service_scope) IS DISTINCT FROM 'object' THEN false
            WHEN c.service_scope -> 'queue_ids' IS NULL THEN true
            WHEN jsonb_typeof(c.service_scope -> 'queue_ids') <> 'array' THEN false
            ELSE p_queue IS NOT NULL AND (c.service_scope -> 'queue_ids') @> to_jsonb(p_queue::text)
          END
  );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION hub_destination_ready(UUID, UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hub_destination_ready(UUID, UUID, UUID) TO omnira_app;
