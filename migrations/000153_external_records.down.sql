-- Rolls back ADR-0049 phase 4. DESTRUCTIVE: external records, their links and requests are lost. Roll back 000154+ first.
SET LOCAL lock_timeout = '5s';
DELETE FROM service_command_receipts WHERE scope IN ('external.update', 'flow.step');
ALTER TABLE service_command_receipts DROP CONSTRAINT IF EXISTS service_command_receipts_target;
ALTER TABLE service_command_receipts DROP COLUMN IF EXISTS external_record_id;
ALTER TABLE service_command_receipts DROP CONSTRAINT IF EXISTS service_command_receipts_scope_check;
ALTER TABLE service_command_receipts ADD CONSTRAINT service_command_receipts_scope_check CHECK (scope IN ('session.open', 'case.open', 'task.create'));
ALTER TABLE service_command_receipts ADD CONSTRAINT service_command_receipts_target CHECK (
  (scope = 'session.open' AND session_id IS NOT NULL AND case_id IS NULL AND task_id IS NULL)
  OR (scope = 'case.open' AND case_id IS NOT NULL AND session_id IS NULL AND task_id IS NULL)
  OR (scope = 'task.create' AND task_id IS NOT NULL AND session_id IS NULL AND case_id IS NULL));
DROP TABLE IF EXISTS external_record_requests;
DROP TABLE IF EXISTS service_external_links;
DROP TABLE IF EXISTS external_records;
DROP FUNCTION IF EXISTS external_record_requests_before_update();
DROP FUNCTION IF EXISTS service_external_links_before_update();
DROP FUNCTION IF EXISTS external_records_before_update();
