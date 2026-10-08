-- Rollback ADR-0025: Service Hub delegation model

DROP INDEX IF EXISTS hub_inbox_items_sla_idx;
DROP INDEX IF EXISTS hub_inbox_items_assigned_idx;
DROP INDEX IF EXISTS hub_inbox_items_updated_at_idx;
DROP INDEX IF EXISTS hub_inbox_items_tenant_idx;
DROP INDEX IF EXISTS hub_inbox_items_hub_tenant_idx;
DROP TABLE IF EXISTS hub_inbox_items;

DROP INDEX IF EXISTS effective_access_grants_status_idx;
DROP INDEX IF EXISTS effective_access_grants_contract_idx;
DROP INDEX IF EXISTS effective_access_grants_hub_idx;
DROP INDEX IF EXISTS effective_access_grants_user_tenant_idx;
DROP TABLE IF EXISTS effective_access_grants;

DROP INDEX IF EXISTS agent_skills_skill_idx;
DROP INDEX IF EXISTS agent_skills_user_idx;
DROP TABLE IF EXISTS agent_skills;

DROP INDEX IF EXISTS skills_hub_idx;
DROP TABLE IF EXISTS skills;

DROP INDEX IF EXISTS work_pool_members_pool_idx;
DROP INDEX IF EXISTS work_pool_members_user_idx;
DROP TABLE IF EXISTS work_pool_members;

DROP INDEX IF EXISTS work_pools_hub_idx;
DROP TABLE IF EXISTS work_pools;

DROP INDEX IF EXISTS hub_tenant_service_contracts_status_idx;
DROP INDEX IF EXISTS hub_tenant_service_contracts_hub_idx;
DROP INDEX IF EXISTS hub_tenant_service_contracts_tenant_idx;
DROP TABLE IF EXISTS hub_tenant_service_contracts;

DROP INDEX IF EXISTS hub_memberships_hub_idx;
DROP INDEX IF EXISTS hub_memberships_user_idx;
DROP TABLE IF EXISTS hub_memberships;

DROP TABLE IF EXISTS service_hubs;

DELETE FROM roles WHERE tenant_id IS NULL AND key = 'hub_agent';
