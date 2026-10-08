-- Rollback ADR-0026/0030: Hub RLS access policies

-- Drop policies (order: bottom-up)
DROP POLICY IF EXISTS hub_inbox_update_system ON hub_inbox_items;
DROP POLICY IF EXISTS hub_inbox_delete_admin ON hub_inbox_items;
DROP POLICY IF EXISTS hub_inbox_update_admin ON hub_inbox_items;
DROP POLICY IF EXISTS hub_inbox_write_admin ON hub_inbox_items;
DROP POLICY IF EXISTS hub_inbox_read_hub_delegation ON hub_inbox_items;
DROP POLICY IF EXISTS hub_inbox_read_direct_tenant ON hub_inbox_items;

DROP POLICY IF EXISTS effective_grants_delete_admin ON effective_access_grants;
DROP POLICY IF EXISTS effective_grants_update_admin ON effective_access_grants;
DROP POLICY IF EXISTS effective_grants_write_admin ON effective_access_grants;
DROP POLICY IF EXISTS effective_grants_read_hub_admin ON effective_access_grants;
DROP POLICY IF EXISTS effective_grants_read_self ON effective_access_grants;
DROP POLICY IF EXISTS effective_grants_read_admin ON effective_access_grants;

DROP POLICY IF EXISTS agent_skills_delete_admin ON agent_skills;
DROP POLICY IF EXISTS agent_skills_update_admin ON agent_skills;
DROP POLICY IF EXISTS agent_skills_write_admin ON agent_skills;
DROP POLICY IF EXISTS agent_skills_read_hub ON agent_skills;
DROP POLICY IF EXISTS agent_skills_read_self ON agent_skills;
DROP POLICY IF EXISTS agent_skills_read_admin ON agent_skills;

DROP POLICY IF EXISTS skills_delete_admin ON skills;
DROP POLICY IF EXISTS skills_update_admin ON skills;
DROP POLICY IF EXISTS skills_write_admin ON skills;
DROP POLICY IF EXISTS skills_read_member ON skills;
DROP POLICY IF EXISTS skills_read_admin ON skills;

DROP POLICY IF EXISTS work_pool_members_delete_admin ON work_pool_members;
DROP POLICY IF EXISTS work_pool_members_update_admin ON work_pool_members;
DROP POLICY IF EXISTS work_pool_members_write_admin ON work_pool_members;
DROP POLICY IF EXISTS work_pool_members_read_member ON work_pool_members;
DROP POLICY IF EXISTS work_pool_members_read_admin ON work_pool_members;

DROP POLICY IF EXISTS work_pools_delete_admin ON work_pools;
DROP POLICY IF EXISTS work_pools_update_admin ON work_pools;
DROP POLICY IF EXISTS work_pools_write_admin ON work_pools;
DROP POLICY IF EXISTS work_pools_read_member ON work_pools;
DROP POLICY IF EXISTS work_pools_read_admin ON work_pools;

DROP POLICY IF EXISTS hub_contracts_delete_admin ON hub_tenant_service_contracts;
DROP POLICY IF EXISTS hub_contracts_update_admin ON hub_tenant_service_contracts;
DROP POLICY IF EXISTS hub_contracts_write_admin ON hub_tenant_service_contracts;
DROP POLICY IF EXISTS hub_contracts_read_member ON hub_tenant_service_contracts;
DROP POLICY IF EXISTS hub_contracts_read_admin ON hub_tenant_service_contracts;

DROP POLICY IF EXISTS hub_memberships_delete_admin ON hub_memberships;
DROP POLICY IF EXISTS hub_memberships_update_admin ON hub_memberships;
DROP POLICY IF EXISTS hub_memberships_write_admin ON hub_memberships;
DROP POLICY IF EXISTS hub_memberships_read_hub_admin ON hub_memberships;
DROP POLICY IF EXISTS hub_memberships_read_self ON hub_memberships;
DROP POLICY IF EXISTS hub_memberships_read_admin ON hub_memberships;

DROP POLICY IF EXISTS service_hubs_delete_admin ON service_hubs;
DROP POLICY IF EXISTS service_hubs_update_admin ON service_hubs;
DROP POLICY IF EXISTS service_hubs_write_admin ON service_hubs;
DROP POLICY IF EXISTS service_hubs_read_member ON service_hubs;
DROP POLICY IF EXISTS service_hubs_read_admin ON service_hubs;

-- Drop helper functions
DROP FUNCTION IF EXISTS current_access_via();
DROP FUNCTION IF EXISTS current_hub_id();
DROP FUNCTION IF EXISTS has_active_hub_access(UUID, UUID, UUID);

-- Disable RLS on Hub tables
ALTER TABLE hub_inbox_items DISABLE ROW LEVEL SECURITY;
ALTER TABLE effective_access_grants DISABLE ROW LEVEL SECURITY;
ALTER TABLE agent_skills DISABLE ROW LEVEL SECURITY;
ALTER TABLE skills DISABLE ROW LEVEL SECURITY;
ALTER TABLE work_pool_members DISABLE ROW LEVEL SECURITY;
ALTER TABLE work_pools DISABLE ROW LEVEL SECURITY;
ALTER TABLE hub_tenant_service_contracts DISABLE ROW LEVEL SECURITY;
ALTER TABLE hub_memberships DISABLE ROW LEVEL SECURITY;
ALTER TABLE service_hubs DISABLE ROW LEVEL SECURITY;
