DELETE FROM role_permissions WHERE permission_key IN ('agent.read','agent.manage');
DELETE FROM permissions WHERE key IN ('agent.read','agent.manage');
DROP TABLE IF EXISTS agent_profiles;
ALTER TABLE memberships DROP CONSTRAINT IF EXISTS memberships_tenant_id_id_uq;
