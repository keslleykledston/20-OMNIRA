-- WhatsApp Cloud API message templates (approved by Meta) synced per connection, and the record of which template a
-- queued outbound message is. A template message reuses the whole outbound pipeline (outbox, delivery job, uncertain
-- handling): the message row carries the rendered text, this table carries what the provider needs to send it.
CREATE TABLE channel_message_templates (
  id                   UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id            UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  connection_id        UUID NOT NULL,
  provider_template_id TEXT NOT NULL DEFAULT '',
  name                 TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 512 AND name ~ '^[a-z0-9_]+$'),
  language             TEXT NOT NULL CHECK (language ~ '^[a-z]{2,3}(_[A-Za-z]{2,4})?$'),
  category             TEXT NOT NULL DEFAULT '',
  status               TEXT NOT NULL DEFAULT '',
  body_text            TEXT NOT NULL DEFAULT '',
  variable_count       INT  NOT NULL DEFAULT 0 CHECK (variable_count BETWEEN 0 AND 20),
  -- sendable: the template needs nothing this version cannot fill (body variables only). Header media/variables and
  -- dynamic URL buttons are not supported yet and the reason is kept so the operator is told, not left guessing.
  sendable             BOOLEAN NOT NULL DEFAULT true,
  unsupported_reason   TEXT NOT NULL DEFAULT '',
  synced_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, connection_id, name, language),
  FOREIGN KEY (tenant_id, connection_id) REFERENCES channel_connections(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX channel_message_templates_conn_idx ON channel_message_templates (tenant_id, connection_id, status);
ALTER TABLE channel_message_templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel_message_templates FORCE ROW LEVEL SECURITY;
CREATE POLICY channel_message_templates_read_tenant ON channel_message_templates FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY channel_message_templates_insert_tenant ON channel_message_templates FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY channel_message_templates_update_tenant ON channel_message_templates FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY channel_message_templates_delete_tenant ON channel_message_templates FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON channel_message_templates TO omnira_app;

CREATE TABLE message_template_sends (
  tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  message_id    UUID NOT NULL,
  template_name TEXT NOT NULL CHECK (char_length(template_name) BETWEEN 1 AND 512 AND template_name ~ '^[a-z0-9_]+$'),
  language      TEXT NOT NULL CHECK (language ~ '^[a-z]{2,3}(_[A-Za-z]{2,4})?$'),
  -- body variables in order ({{1}}, {{2}}, ...). Written once when the message is queued, never changed.
  params        JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(params) = 'array'),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, message_id),
  FOREIGN KEY (tenant_id, message_id) REFERENCES messages(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE message_template_sends ENABLE ROW LEVEL SECURITY;
ALTER TABLE message_template_sends FORCE ROW LEVEL SECURITY;
CREATE POLICY message_template_sends_read_tenant ON message_template_sends FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY message_template_sends_insert_tenant ON message_template_sends FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
GRANT SELECT, INSERT ON message_template_sends TO omnira_app;
