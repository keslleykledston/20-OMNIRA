-- M04.3: one explicit initial queue per tenant; no name-based inference.
ALTER TABLE queues ADD COLUMN is_default BOOLEAN NOT NULL DEFAULT false;
CREATE UNIQUE INDEX queues_one_default_per_tenant
  ON queues(tenant_id) WHERE is_default;
