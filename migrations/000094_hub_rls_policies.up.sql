-- ADR-0026/0030: Hub RLS access policies
-- Enforces that Hub agents can only access delegated tenants via active contracts

-- Helper function: check if user has active Hub access to a Tenant
CREATE OR REPLACE FUNCTION has_active_hub_access(hub_id UUID, user_id UUID, tenant_id UUID) RETURNS BOOLEAN AS $$
  SELECT EXISTS (
    SELECT 1
    FROM effective_access_grants eag
    WHERE eag.hub_id = $1
      AND eag.user_id = $2
      AND eag.tenant_id = $3
      AND eag.status = 'active'
      AND (eag.valid_until IS NULL OR eag.valid_until > now())
      AND EXISTS (
        SELECT 1
        FROM hub_tenant_service_contracts hc
        WHERE hc.id = eag.service_contract_id
          AND hc.status = 'active'
          AND (hc.valid_until IS NULL OR hc.valid_until > now())
      )
  )
$$ LANGUAGE SQL STABLE;

-- Helper: resolve hub_id from current context (if set)
CREATE OR REPLACE FUNCTION current_hub_id() RETURNS UUID AS $$
  SELECT NULLIF(current_setting('omnira.hub_id', true), '')::UUID
$$ LANGUAGE SQL STABLE;

-- Helper: resolve access_via mode
CREATE OR REPLACE FUNCTION current_access_via() RETURNS TEXT AS $$
  SELECT COALESCE(current_setting('omnira.access_via', true), 'direct')
$$ LANGUAGE SQL STABLE;

-- Enable RLS on all Hub tables
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

-- service_hubs policies
-- System admin can read all hubs
CREATE POLICY service_hubs_read_admin ON service_hubs
  FOR SELECT USING (is_system_admin());

-- Hub members can read their hub
CREATE POLICY service_hubs_read_member ON service_hubs
  FOR SELECT USING (
    EXISTS (
      SELECT 1 FROM hub_memberships hm
      WHERE hm.hub_id = service_hubs.id
        AND hm.user_id = current_user_id()
    )
  );

-- System admin can insert/update/delete hubs
CREATE POLICY service_hubs_write_admin ON service_hubs
  FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY service_hubs_update_admin ON service_hubs
  FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY service_hubs_delete_admin ON service_hubs
  FOR DELETE USING (is_system_admin());

-- hub_memberships policies
-- System admin can read all
CREATE POLICY hub_memberships_read_admin ON hub_memberships
  FOR SELECT USING (is_system_admin());

-- User can read their own memberships
CREATE POLICY hub_memberships_read_self ON hub_memberships
  FOR SELECT USING (user_id = current_user_id());

-- Hub admin can read members of their hub
CREATE POLICY hub_memberships_read_hub_admin ON hub_memberships
  FOR SELECT USING (
    is_system_admin() OR EXISTS (
      SELECT 1 FROM hub_memberships hm2
        INNER JOIN roles r ON r.id = hm2.role_id
      WHERE hm2.hub_id = hub_memberships.hub_id
        AND hm2.user_id = current_user_id()
        AND (r.key = 'hub_admin' OR r.key = 'system_admin')
    )
  );

-- System admin inserts/updates/deletes
CREATE POLICY hub_memberships_write_admin ON hub_memberships
  FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY hub_memberships_update_admin ON hub_memberships
  FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY hub_memberships_delete_admin ON hub_memberships
  FOR DELETE USING (is_system_admin());

-- hub_tenant_service_contracts policies
-- Read: system admin or hub member of that hub
CREATE POLICY hub_contracts_read_admin ON hub_tenant_service_contracts
  FOR SELECT USING (is_system_admin());

CREATE POLICY hub_contracts_read_member ON hub_tenant_service_contracts
  FOR SELECT USING (
    EXISTS (
      SELECT 1 FROM hub_memberships hm
      WHERE hm.hub_id = hub_tenant_service_contracts.hub_id
        AND hm.user_id = current_user_id()
    )
  );

-- System admin writes
CREATE POLICY hub_contracts_write_admin ON hub_tenant_service_contracts
  FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY hub_contracts_update_admin ON hub_tenant_service_contracts
  FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY hub_contracts_delete_admin ON hub_tenant_service_contracts
  FOR DELETE USING (is_system_admin());

-- work_pools: Hub members can read their hub's pools
CREATE POLICY work_pools_read_admin ON work_pools
  FOR SELECT USING (is_system_admin());

CREATE POLICY work_pools_read_member ON work_pools
  FOR SELECT USING (
    EXISTS (
      SELECT 1 FROM hub_memberships hm
      WHERE hm.hub_id = work_pools.hub_id
        AND hm.user_id = current_user_id()
    )
  );

CREATE POLICY work_pools_write_admin ON work_pools
  FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY work_pools_update_admin ON work_pools
  FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY work_pools_delete_admin ON work_pools
  FOR DELETE USING (is_system_admin());

-- work_pool_members: Hub members can read their hub's members
CREATE POLICY work_pool_members_read_admin ON work_pool_members
  FOR SELECT USING (is_system_admin());

CREATE POLICY work_pool_members_read_member ON work_pool_members
  FOR SELECT USING (
    EXISTS (
      SELECT 1 FROM work_pools wp
        INNER JOIN hub_memberships hm ON hm.hub_id = wp.hub_id
      WHERE wp.id = work_pool_members.work_pool_id
        AND hm.user_id = current_user_id()
    )
  );

CREATE POLICY work_pool_members_write_admin ON work_pool_members
  FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY work_pool_members_update_admin ON work_pool_members
  FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY work_pool_members_delete_admin ON work_pool_members
  FOR DELETE USING (is_system_admin());

-- skills: Hub members can read their hub's skills
CREATE POLICY skills_read_admin ON skills
  FOR SELECT USING (is_system_admin());

CREATE POLICY skills_read_member ON skills
  FOR SELECT USING (
    EXISTS (
      SELECT 1 FROM hub_memberships hm
      WHERE hm.hub_id = skills.hub_id
        AND hm.user_id = current_user_id()
    )
  );

CREATE POLICY skills_write_admin ON skills
  FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY skills_update_admin ON skills
  FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY skills_delete_admin ON skills
  FOR DELETE USING (is_system_admin());

-- agent_skills: user can read their own, hub members can read their hub's
CREATE POLICY agent_skills_read_admin ON agent_skills
  FOR SELECT USING (is_system_admin());

CREATE POLICY agent_skills_read_self ON agent_skills
  FOR SELECT USING (user_id = current_user_id());

CREATE POLICY agent_skills_read_hub ON agent_skills
  FOR SELECT USING (
    EXISTS (
      SELECT 1 FROM skills s
        INNER JOIN hub_memberships hm ON hm.hub_id = s.hub_id
      WHERE s.id = agent_skills.skill_id
        AND hm.user_id = current_user_id()
    )
  );

CREATE POLICY agent_skills_write_admin ON agent_skills
  FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY agent_skills_update_admin ON agent_skills
  FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY agent_skills_delete_admin ON agent_skills
  FOR DELETE USING (is_system_admin());

-- effective_access_grants: user can read their own, hub admins can see their hub's
CREATE POLICY effective_grants_read_admin ON effective_access_grants
  FOR SELECT USING (is_system_admin());

CREATE POLICY effective_grants_read_self ON effective_access_grants
  FOR SELECT USING (user_id = current_user_id());

CREATE POLICY effective_grants_read_hub_admin ON effective_access_grants
  FOR SELECT USING (
    EXISTS (
      SELECT 1 FROM hub_memberships hm2
        INNER JOIN roles r ON r.id = hm2.role_id
      WHERE hm2.hub_id = effective_access_grants.hub_id
        AND hm2.user_id = current_user_id()
        AND (r.key = 'hub_admin' OR r.key = 'system_admin')
    )
  );

CREATE POLICY effective_grants_write_admin ON effective_access_grants
  FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY effective_grants_update_admin ON effective_access_grants
  FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY effective_grants_delete_admin ON effective_access_grants
  FOR DELETE USING (is_system_admin());

-- hub_inbox_items: visible only if user has active access to the tenant (via contract)
-- Either direct tenant membership OR hub delegation
CREATE POLICY hub_inbox_read_direct_tenant ON hub_inbox_items
  FOR SELECT USING (
    has_active_membership(tenant_id, current_user_id())
  );

CREATE POLICY hub_inbox_read_hub_delegation ON hub_inbox_items
  FOR SELECT USING (
    has_active_hub_access(hub_id, current_user_id(), tenant_id)
  );

-- System admin can modify inbox
CREATE POLICY hub_inbox_write_admin ON hub_inbox_items
  FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY hub_inbox_update_admin ON hub_inbox_items
  FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY hub_inbox_delete_admin ON hub_inbox_items
  FOR DELETE USING (is_system_admin());

-- Worker can update inbox (system context)
CREATE POLICY hub_inbox_update_system ON hub_inbox_items
  FOR UPDATE USING (is_system_admin());
