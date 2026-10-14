ALTER TABLE conversation_closures DROP CONSTRAINT IF EXISTS conversation_closures_closer_shape, DROP CONSTRAINT IF EXISTS conversation_closures_installation_fk;
DELETE FROM conversation_closures WHERE source = 'external_app';
ALTER TABLE conversation_closures DROP COLUMN IF EXISTS closed_by_installation_id;
ALTER TABLE conversation_closures DROP CONSTRAINT IF EXISTS conversation_closures_source_check;
ALTER TABLE conversation_closures ADD CONSTRAINT conversation_closures_source_check CHECK (source IN ('agent', 'supervisor', 'system'));
DROP FUNCTION IF EXISTS external_command_receipts_purge(INTERVAL);
DROP TABLE IF EXISTS external_command_receipts;
