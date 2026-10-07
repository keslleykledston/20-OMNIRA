-- WhatsApp interactive messages (reply buttons / list) queued by the bot. Like a template send, the message row carries
-- the full numbered-text menu (what the inbox shows, and what is sent as plain text on providers without buttons) and this
-- table carries the options the provider needs to render buttons. Written once with the message, never changed.
CREATE TABLE message_interactive_sends (
  tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  message_id UUID NOT NULL,
  -- the question shown above the buttons (the menu text without the numbered lines)
  body       TEXT NOT NULL CHECK (char_length(btrim(body)) BETWEEN 1 AND 1024),
  list_label TEXT NOT NULL DEFAULT '' CHECK (char_length(list_label) <= 20),
  options    JSONB NOT NULL CHECK (jsonb_typeof(options) = 'array' AND jsonb_array_length(options) BETWEEN 1 AND 10),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, message_id),
  FOREIGN KEY (tenant_id, message_id) REFERENCES messages(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE message_interactive_sends ENABLE ROW LEVEL SECURITY;
ALTER TABLE message_interactive_sends FORCE ROW LEVEL SECURITY;
CREATE POLICY message_interactive_sends_read_tenant ON message_interactive_sends FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY message_interactive_sends_insert_tenant ON message_interactive_sends FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
GRANT SELECT, INSERT ON message_interactive_sends TO omnira_app;
