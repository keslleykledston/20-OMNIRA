DROP INDEX IF EXISTS idx_audit_events_causation_id;
ALTER TABLE audit_events DROP COLUMN IF EXISTS causation_id;
