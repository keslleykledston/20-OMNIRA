-- Rolls back ADR-0049 phase 1. DESTRUCTIVE for the data created since: only for an environment where the feature never ran or its data may be dropped.
-- (Later migrations of the feature, 000151+, must be rolled back first.)
SET LOCAL lock_timeout = '5s';
DROP TABLE IF EXISTS service_command_receipts;
DROP TABLE IF EXISTS service_session_episodes;
DROP TABLE IF EXISTS service_sessions;
DROP TABLE IF EXISTS service_resolution_codes;
DROP TABLE IF EXISTS service_categories;
DROP TABLE IF EXISTS service_counters;
DROP FUNCTION IF EXISTS service_session_episodes_before_update();
DROP FUNCTION IF EXISTS service_session_episodes_before_insert();
DROP FUNCTION IF EXISTS service_sessions_before_update();
DROP FUNCTION IF EXISTS service_sessions_before_insert();
DROP FUNCTION IF EXISTS service_conversation_open_locked(UUID, UUID);
DROP FUNCTION IF EXISTS service_read_tenants();
DELETE FROM role_permissions WHERE permission_key LIKE 'service.%';
DELETE FROM permissions WHERE key LIKE 'service.%';
