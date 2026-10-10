DROP TRIGGER IF EXISTS follow_up_items_acting_columns ON follow_up_items;
DROP FUNCTION IF EXISTS follow_up_items_acting_columns();
DROP POLICY IF EXISTS follow_up_items_acting_no_insert ON follow_up_items;
DROP POLICY IF EXISTS conversation_closures_acting_no_insert ON conversation_closures;
DROP POLICY IF EXISTS follow_up_items_acting_resolve ON follow_up_items;
DROP POLICY IF EXISTS follow_up_items_acting_history ON follow_up_items;
DROP POLICY IF EXISTS conversation_closures_acting_history ON conversation_closures;
DROP POLICY IF EXISTS follow_up_items_resolve_delegated ON follow_up_items;
DROP POLICY IF EXISTS follow_up_items_read_delegated ON follow_up_items;
DROP POLICY IF EXISTS conversation_closures_read_delegated ON conversation_closures;
DELETE FROM permission_domains WHERE permission_key IN ('attendance.history', 'followup.resolve');
-- a grant or a contract ceiling that already carries these keys loses them at once (delegated_permissions only returns keys that have a domain row)
DELETE FROM role_permissions WHERE permission_key IN ('attendance.history', 'followup.resolve');
DELETE FROM permissions WHERE key IN ('attendance.history', 'followup.resolve');
