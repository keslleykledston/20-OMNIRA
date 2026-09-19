-- Rollback: audit and outbox

DROP TABLE IF EXISTS outbox_events CASCADE;
DROP TABLE IF EXISTS audit_events CASCADE;
