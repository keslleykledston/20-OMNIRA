ALTER TABLE flows DROP CONSTRAINT flows_active_version_fk;
ALTER TABLE flows ADD CONSTRAINT flows_active_version_fk
  FOREIGN KEY (tenant_id, active_version_id) REFERENCES flow_versions(tenant_id, id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE flow_versions DROP CONSTRAINT flow_versions_tenant_flow_id_key;
