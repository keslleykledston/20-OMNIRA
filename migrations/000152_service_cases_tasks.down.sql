-- Rolls back ADR-0049 phase 3. DESTRUCTIVE: cases, tasks, their links and the receipts that point to them are lost. Roll back 000153+ first.
SET LOCAL lock_timeout = '5s';
ALTER TABLE context_events DROP CONSTRAINT IF EXISTS context_events_tenant_id_case_id_fkey;
DELETE FROM service_command_receipts WHERE scope IN ('case.open', 'task.create');
ALTER TABLE service_command_receipts DROP CONSTRAINT IF EXISTS service_command_receipts_target;
ALTER TABLE service_command_receipts DROP COLUMN IF EXISTS case_id, DROP COLUMN IF EXISTS task_id;
ALTER TABLE service_command_receipts ADD CONSTRAINT service_command_receipts_target CHECK (scope = 'session.open' AND session_id IS NOT NULL);
DROP TABLE IF EXISTS service_tasks;
DROP TABLE IF EXISTS service_case_sessions;
DROP TABLE IF EXISTS service_cases;
DROP FUNCTION IF EXISTS service_tasks_before_update();
DROP FUNCTION IF EXISTS service_cases_assignee_check();
DROP FUNCTION IF EXISTS service_tasks_assignee_check();
DROP FUNCTION IF EXISTS service_active_member(UUID, UUID);
DROP FUNCTION IF EXISTS service_cases_before_update();
