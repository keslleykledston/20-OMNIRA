-- U3: durable dedupe inbox for verified WAHA webhooks.
-- Store message/event identity and digest only; never raw provider payload.
CREATE TABLE channel_webhook_events (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  connection_id UUID NOT NULL REFERENCES channel_connections(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  provider_event_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  payload_digest TEXT NOT NULL,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (connection_id, provider_event_id)
);

CREATE INDEX idx_channel_webhook_events_tenant_id ON channel_webhook_events(tenant_id);

ALTER TABLE channel_webhook_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel_webhook_events FORCE ROW LEVEL SECURITY;

CREATE POLICY channel_webhook_events_read_tenant ON channel_webhook_events FOR SELECT
  USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY channel_webhook_events_insert_tenant ON channel_webhook_events FOR INSERT
  WITH CHECK (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin());

GRANT SELECT, INSERT ON channel_webhook_events TO omnira_app;
