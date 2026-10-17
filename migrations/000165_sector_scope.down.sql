SET LOCAL lock_timeout = '5s';
DROP FUNCTION IF EXISTS conversation_in_sector_scope(UUID, UUID, UUID);
DROP FUNCTION IF EXISTS sector_scope_restricts(UUID, UUID);
DELETE FROM role_permissions WHERE permission_key = 'conversation.read_all_queues';
DELETE FROM permissions WHERE key = 'conversation.read_all_queues';
DELETE FROM tenant_entitlements WHERE capability = 'sector_scope';
