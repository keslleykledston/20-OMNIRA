-- ADR-0058: at most one group per tenant may be the SLA alert channel (mensagem de verdade enviada ao WhatsApp,
-- a única exceção à regra "nada é enviado a grupo" das ADR-0015/0055/0056). Two CHECKs enforce the safeguards at
-- the database, not only in the handler: never more than one per tenant, and never a group already linked to a
-- customer_account (ADR-0018) — in either order of the two updates.
ALTER TABLE wa_groups ADD COLUMN is_alert_channel BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE wa_groups ADD COLUMN alert_channel_set_by UUID REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE wa_groups ADD COLUMN alert_channel_set_at TIMESTAMPTZ;

ALTER TABLE wa_groups ADD CONSTRAINT wa_groups_alert_channel_not_client_group
  CHECK (NOT is_alert_channel OR account_id IS NULL);

CREATE UNIQUE INDEX idx_wa_groups_one_alert_channel_per_tenant ON wa_groups (tenant_id) WHERE is_alert_channel;
