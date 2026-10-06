-- FLOW-003 (Codex review of ADR-0019): a flow's active version must belong to THAT flow, not merely to the same tenant.
-- Forward-only fix to 000082 (already applied), so live and fresh databases end in the same schema.
ALTER TABLE flow_versions ADD CONSTRAINT flow_versions_tenant_flow_id_key UNIQUE (tenant_id, flow_id, id);
ALTER TABLE flows DROP CONSTRAINT flows_active_version_fk;
ALTER TABLE flows ADD CONSTRAINT flows_active_version_fk
  FOREIGN KEY (tenant_id, id, active_version_id) REFERENCES flow_versions(tenant_id, flow_id, id) DEFERRABLE INITIALLY DEFERRED;
