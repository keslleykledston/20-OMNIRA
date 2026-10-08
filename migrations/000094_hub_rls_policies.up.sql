-- ADR-0026/0030: Hub RLS policies.
--
-- Same technique as 000004: helpers that read RLS-protected tables are SECURITY DEFINER (owned by the
-- migration owner, which bypasses RLS), otherwise a policy on hub_memberships that reads hub_memberships
-- aborts with "infinite recursion detected in policy". Every helper takes the user as an explicit
-- argument; callers pass current_user_id() (the session GUC set by the application), so nothing here
-- depends on is_system_admin() for a Hub agent.

-- Hardening applied to every helper below (found in adversarial review, reproduced by tests):
--  * SET search_path = pg_catalog, public, pg_temp and schema-qualified relations/functions: with the
--    usual "SET search_path = public" the session's pg_temp is searched FIRST for relations, so a TEMP table
--    named like a real one would shadow it inside a SECURITY DEFINER function and forge access;
--  * the p_user_id argument must be the session user (or a system session): otherwise any SQL-capable session
--    could ask "does user X have access to tenant Y?" about somebody else.

CREATE OR REPLACE FUNCTION is_hub_member(p_hub_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND EXISTS (SELECT 1 FROM public.hub_memberships WHERE hub_id = p_hub_id AND user_id = p_user_id);
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

CREATE OR REPLACE FUNCTION is_hub_admin(p_hub_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND EXISTS (
       SELECT 1
       FROM public.hub_memberships hm
       JOIN public.roles r ON r.id = hm.role_id
       WHERE hm.hub_id = p_hub_id AND hm.user_id = p_user_id
         AND r.tenant_id IS NULL AND r.key = 'hub_admin'
     );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

-- The single definition of "this user may act on this tenant through a Hub".
-- ALL of the following must hold at query time (now()):
--   * the user is still a member of the grant's hub;
--   * the hub is active;
--   * the grant is active and inside its validity window;
--   * the service contract it points at is active and inside ITS validity window;
--   * the contract scope is a JSON object (anything else denies), and if it declares queue_ids the
--     resource's queue is on that allowlist (a resource without a queue is NOT covered by a restricted
--     contract; a malformed value denies).
-- p_hub_id, when given, additionally pins the hub (used for rows that carry a hub_id).
-- p_check_scope => false is for resources that are not bound to a queue (the tenant row itself, or a row whose
-- queue is enforced through its parent): the grant and contract must still be live, only the queue
-- allowlist is skipped. Queue-bound resources (conversations) always use the default (true).
CREATE OR REPLACE FUNCTION has_active_hub_access(
  p_user_id UUID, p_tenant_id UUID, p_queue_id UUID DEFAULT NULL, p_hub_id UUID DEFAULT NULL, p_check_scope BOOLEAN DEFAULT true
) RETURNS BOOLEAN AS $$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND EXISTS (
    SELECT 1
    FROM public.effective_access_grants g
    JOIN public.hub_tenant_service_contracts c ON c.id = g.service_contract_id
    JOIN public.service_hubs h ON h.id = g.hub_id
    JOIN public.hub_memberships hm ON hm.hub_id = g.hub_id AND hm.user_id = g.user_id
    WHERE g.user_id = p_user_id
      AND g.tenant_id = p_tenant_id
      AND (p_hub_id IS NULL OR g.hub_id = p_hub_id)
      AND h.status = 'active'
      AND g.status = 'active' AND g.valid_from <= now() AND (g.valid_until IS NULL OR g.valid_until > now())
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

-- Lets an agent read the contract row behind a LIVE grant (not an expired/revoked one, not a suspended hub).
CREATE OR REPLACE FUNCTION has_active_grant_on_contract(p_contract_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND EXISTS (
       SELECT 1 FROM public.hub_tenant_service_contracts c
       WHERE c.id = p_contract_id
         AND public.has_active_hub_access(p_user_id, c.tenant_id, NULL, c.hub_id, false)
     );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

REVOKE EXECUTE ON FUNCTION is_hub_member(UUID, UUID) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION is_hub_admin(UUID, UUID) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION has_active_hub_access(UUID, UUID, UUID, UUID, BOOLEAN) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION has_active_grant_on_contract(UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION is_hub_member(UUID, UUID) TO omnira_app;
GRANT EXECUTE ON FUNCTION is_hub_admin(UUID, UUID) TO omnira_app;
GRANT EXECUTE ON FUNCTION has_active_hub_access(UUID, UUID, UUID, UUID, BOOLEAN) TO omnira_app;
GRANT EXECUTE ON FUNCTION has_active_grant_on_contract(UUID, UUID) TO omnira_app;

ALTER TABLE service_hubs ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_hubs FORCE ROW LEVEL SECURITY;
ALTER TABLE hub_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_memberships FORCE ROW LEVEL SECURITY;
ALTER TABLE hub_tenant_service_contracts ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_tenant_service_contracts FORCE ROW LEVEL SECURITY;
ALTER TABLE work_pools ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_pools FORCE ROW LEVEL SECURITY;
ALTER TABLE work_pool_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_pool_members FORCE ROW LEVEL SECURITY;
ALTER TABLE skills ENABLE ROW LEVEL SECURITY;
ALTER TABLE skills FORCE ROW LEVEL SECURITY;
ALTER TABLE agent_skills ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_skills FORCE ROW LEVEL SECURITY;
ALTER TABLE effective_access_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE effective_access_grants FORCE ROW LEVEL SECURITY;
ALTER TABLE hub_inbox_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_inbox_items FORCE ROW LEVEL SECURITY;

-- Writes to every Hub table are system-admin only for now (provisioning is an admin/back-office action);
-- no Hub agent can create or widen their own access.

-- service_hubs
CREATE POLICY service_hubs_read ON service_hubs FOR SELECT
  USING (is_system_admin() OR is_hub_member(id, current_user_id()));
CREATE POLICY service_hubs_insert ON service_hubs FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY service_hubs_update ON service_hubs FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY service_hubs_delete ON service_hubs FOR DELETE USING (is_system_admin());

-- hub_memberships: own rows, or every row of a hub you administer
CREATE POLICY hub_memberships_read ON hub_memberships FOR SELECT
  USING (is_system_admin() OR user_id = current_user_id() OR is_hub_admin(hub_id, current_user_id()));
CREATE POLICY hub_memberships_insert ON hub_memberships FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY hub_memberships_update ON hub_memberships FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY hub_memberships_delete ON hub_memberships FOR DELETE USING (is_system_admin());

-- hub_tenant_service_contracts: hub admins see the hub's contracts; an agent sees only the contracts
-- behind their own active grants (not the list of every tenant the hub serves)
CREATE POLICY hub_contracts_read ON hub_tenant_service_contracts FOR SELECT
  USING (is_system_admin() OR is_hub_admin(hub_id, current_user_id()) OR has_active_grant_on_contract(id, current_user_id()));
CREATE POLICY hub_contracts_insert ON hub_tenant_service_contracts FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY hub_contracts_update ON hub_tenant_service_contracts FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY hub_contracts_delete ON hub_tenant_service_contracts FOR DELETE USING (is_system_admin());

-- effective_access_grants: own grants, or every grant of a hub you administer
CREATE POLICY effective_grants_read ON effective_access_grants FOR SELECT
  USING (is_system_admin() OR user_id = current_user_id() OR is_hub_admin(hub_id, current_user_id()));
CREATE POLICY effective_grants_insert ON effective_access_grants FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY effective_grants_update ON effective_access_grants FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY effective_grants_delete ON effective_access_grants FOR DELETE USING (is_system_admin());

-- work_pools / skills: hub-level reference data, readable by hub members
CREATE POLICY work_pools_read ON work_pools FOR SELECT
  USING (is_system_admin() OR is_hub_member(hub_id, current_user_id()));
CREATE POLICY work_pools_insert ON work_pools FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY work_pools_update ON work_pools FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY work_pools_delete ON work_pools FOR DELETE USING (is_system_admin());

CREATE POLICY skills_read ON skills FOR SELECT
  USING (is_system_admin() OR is_hub_member(hub_id, current_user_id()));
CREATE POLICY skills_insert ON skills FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY skills_update ON skills FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY skills_delete ON skills FOR DELETE USING (is_system_admin());

-- work_pool_members / agent_skills: own rows, or rows whose parent (pool / skill) is visible to the caller.
-- The EXISTS reads work_pools / skills under the caller's RLS, which uses only the DEFINER helpers: no recursion.
CREATE POLICY work_pool_members_read ON work_pool_members FOR SELECT
  USING (is_system_admin() OR user_id = current_user_id()
         OR EXISTS (SELECT 1 FROM work_pools wp WHERE wp.id = work_pool_members.work_pool_id));
CREATE POLICY work_pool_members_insert ON work_pool_members FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY work_pool_members_update ON work_pool_members FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY work_pool_members_delete ON work_pool_members FOR DELETE USING (is_system_admin());

CREATE POLICY agent_skills_read ON agent_skills FOR SELECT
  USING (is_system_admin() OR user_id = current_user_id()
         OR EXISTS (SELECT 1 FROM skills s WHERE s.id = agent_skills.skill_id));
CREATE POLICY agent_skills_insert ON agent_skills FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY agent_skills_update ON agent_skills FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY agent_skills_delete ON agent_skills FOR DELETE USING (is_system_admin());

-- hub_inbox_items (read model, a PROJECTION that can be stale). Delegated only: a user who is a hub member and a
-- direct tenant member but holds no live grant gets the tenant through the tenant inbox, not through the hub.
-- The row is visible only if the caller holds live access (grant, contract, hub) AND can currently read the
-- parent conversation: that read goes through conversations_read_hub_delegation, which scopes by the
-- conversation's REAL queue, so a stale queue_id on the projection cannot widen access.
CREATE POLICY hub_inbox_read ON hub_inbox_items FOR SELECT
  USING (
    is_system_admin()
    OR (
      has_active_hub_access(current_user_id(), tenant_id, NULL, hub_id, false)
      AND EXISTS (SELECT 1 FROM conversations c WHERE c.tenant_id = hub_inbox_items.tenant_id AND c.id = hub_inbox_items.conversation_id)
    )
  );
CREATE POLICY hub_inbox_insert ON hub_inbox_items FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY hub_inbox_update ON hub_inbox_items FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY hub_inbox_delete ON hub_inbox_items FOR DELETE USING (is_system_admin());
