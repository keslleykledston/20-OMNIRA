-- M03: tenant-owned conversation, message and ticket foundation.
-- Forward-fix for databases that already applied the initial M02 draft before
-- this migration was released; fresh installs also converge to the same form.
ALTER TABLE contacts DROP CONSTRAINT IF EXISTS contacts_phone_e164_format;
ALTER TABLE contacts ADD CONSTRAINT contacts_phone_e164_format
  CHECK (phone_e164 ~ '^\+[1-9][0-9]{6,14}$');
CREATE UNIQUE INDEX IF NOT EXISTS contacts_tenant_id_uq ON contacts(tenant_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS channel_connections_tenant_id_uq ON channel_connections(tenant_id, id);

CREATE TABLE conversations (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  contact_id UUID NOT NULL,
  channel_connection_id UUID,
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  title TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  closed_at TIMESTAMPTZ,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, contact_id) REFERENCES contacts(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, channel_connection_id) REFERENCES channel_connections(tenant_id, id) ON DELETE RESTRICT
);

CREATE INDEX idx_conversations_tenant_updated ON conversations(tenant_id, updated_at DESC, id DESC);
CREATE INDEX idx_conversations_tenant_contact ON conversations(tenant_id, contact_id);

CREATE TABLE messages (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id UUID NOT NULL,
  direction TEXT NOT NULL CHECK (direction IN ('inbound', 'outbound')),
  message_type TEXT NOT NULL DEFAULT 'text' CHECK (message_type IN ('text', 'image', 'video', 'audio', 'document', 'sticker')),
  body TEXT NOT NULL DEFAULT '',
  provider_message_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'received' CHECK (status IN ('received', 'queued', 'sent', 'delivered', 'read', 'failed')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_messages_tenant_conversation_created ON messages(tenant_id, conversation_id, created_at, id);
CREATE UNIQUE INDEX messages_provider_id_uq ON messages(tenant_id, provider_message_id)
  WHERE provider_message_id <> '';

CREATE TABLE tickets (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id UUID NOT NULL,
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'in_progress', 'waiting', 'resolved', 'closed')),
  priority TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('critical', 'high', 'medium', 'low')),
  subject TEXT NOT NULL DEFAULT '',
  assigned_to UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at TIMESTAMPTZ,
  closed_at TIMESTAMPTZ,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_tickets_tenant_status_updated ON tickets(tenant_id, status, updated_at DESC, id DESC);

DO $m03$
DECLARE
  table_name TEXT;
BEGIN
  FOREACH table_name IN ARRAY ARRAY['conversations', 'messages', 'tickets'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format('CREATE POLICY %I_read_tenant ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', table_name, table_name);
    EXECUTE format('CREATE POLICY %I_insert_tenant ON %I FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', table_name, table_name);
    EXECUTE format('CREATE POLICY %I_update_tenant ON %I FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', table_name, table_name);
    EXECUTE format('CREATE POLICY %I_delete_tenant ON %I FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', table_name, table_name);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO omnira_app', table_name);
  END LOOP;
END $m03$;
