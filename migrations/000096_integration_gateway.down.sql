-- Rollback ADR-0028/0029/0031: Integration Gateway

DROP INDEX IF EXISTS webhook_dedup_instance_idx;
DROP TABLE IF EXISTS webhook_deduplication;

DROP INDEX IF EXISTS external_action_receipts_correlation_idx;
DROP INDEX IF EXISTS external_action_receipts_status_idx;
DROP INDEX IF EXISTS external_action_receipts_idempotency_idx;
DROP INDEX IF EXISTS external_action_receipts_tenant_idx;
DROP TABLE IF EXISTS external_action_receipts;

DROP INDEX IF EXISTS integration_capabilities_enabled_idx;
DROP INDEX IF EXISTS integration_capabilities_instance_idx;
DROP TABLE IF EXISTS integration_capabilities;

DROP INDEX IF EXISTS integration_instances_type_idx;
DROP INDEX IF EXISTS integration_instances_status_idx;
DROP INDEX IF EXISTS integration_instances_tenant_idx;
DROP TABLE IF EXISTS integration_instances;
