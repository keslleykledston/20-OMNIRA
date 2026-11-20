DELETE FROM role_permissions WHERE permission_key IN ('group.ai.read','group.ai.manage','group.import','group.memory.erase');
DELETE FROM permissions WHERE key IN ('group.ai.read','group.ai.manage','group.import','group.memory.erase');
DROP TABLE IF EXISTS wa_group_memory_state;
DROP TABLE IF EXISTS wa_group_memory_item_events;
DROP FUNCTION IF EXISTS wa_group_memory_item_events_immutable();
DROP TABLE IF EXISTS wa_group_memory_items;
ALTER TABLE wa_groups DROP COLUMN IF EXISTS ai_enabled_at, DROP COLUMN IF EXISTS ai_enabled_by, DROP COLUMN IF EXISTS ai_enabled;
