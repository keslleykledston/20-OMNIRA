-- Reliability hardening (pre-pilot). aggregate_id/correlation_id/causation_id
-- are TEXT (loosely typed) but every current writer (OutboxService.Store,
-- PostgresInboundStore.RouteNew, IAM4.2-B0's LivenessRepository.Retrigger)
-- always writes a UUID string, and internal/worker/publisher's scan assumes
-- exactly that. A row where one of these is present but not a valid UUID
-- (e.g. accidental corruption at write time) aborted the entire
-- FindUnpublished batch scan, permanently blocking every valid event behind
-- it in created_at order — confirmed live during IAM4.2 pilot fixture
-- verification. Containment (internal/outbox/adapters/postgres.go) now
-- isolates such a row instead of letting it wedge the publisher; these
-- columns are the durable, queryable record of that isolation — the row is
-- never deleted, only excluded from the hot unpublished scan.
ALTER TABLE outbox_events ADD COLUMN quarantined_at TIMESTAMPTZ;
ALTER TABLE outbox_events ADD COLUMN quarantine_reason TEXT;

-- Matches the publisher's actual WHERE clause exactly.
CREATE INDEX idx_outbox_events_unpublished ON outbox_events(created_at ASC)
  WHERE published_at IS NULL AND quarantined_at IS NULL;

-- Prevention: NOT VALID so this ships safely regardless of any existing
-- environment's historical data (validating against rows already known to
-- violate it would fail the migration). New/updated rows are enforced
-- immediately regardless of NOT VALID — only pre-existing rows are exempt
-- until a later, deliberate `VALIDATE CONSTRAINT` (after any historical bad
-- rows are quarantined or cleaned, tracked as dev-data-hygiene debt).
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_aggregate_id_uuid_chk
  CHECK (aggregate_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') NOT VALID;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_correlation_id_uuid_chk
  CHECK (correlation_id IS NULL OR correlation_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') NOT VALID;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_causation_id_uuid_chk
  CHECK (causation_id IS NULL OR causation_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') NOT VALID;
