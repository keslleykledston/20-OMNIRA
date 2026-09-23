DROP INDEX IF EXISTS tickets_tenant_provider_external_id_uq;
ALTER TABLE tickets
  DROP COLUMN provider,
  DROP COLUMN external_ticket_id,
  DROP COLUMN external_status,
  DROP COLUMN external_status_label,
  DROP COLUMN sync_status,
  DROP COLUMN last_synced_at;
