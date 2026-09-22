ALTER TABLE outbox_events DROP CONSTRAINT IF EXISTS outbox_events_causation_id_uuid_chk;
ALTER TABLE outbox_events DROP CONSTRAINT IF EXISTS outbox_events_correlation_id_uuid_chk;
ALTER TABLE outbox_events DROP CONSTRAINT IF EXISTS outbox_events_aggregate_id_uuid_chk;
DROP INDEX IF EXISTS idx_outbox_events_unpublished;
ALTER TABLE outbox_events DROP COLUMN IF EXISTS quarantine_reason;
ALTER TABLE outbox_events DROP COLUMN IF EXISTS quarantined_at;
