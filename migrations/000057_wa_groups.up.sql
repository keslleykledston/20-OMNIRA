-- ADR-0015: read-only WhatsApp group messages, kept apart from contacts/conversations/messages
-- because a group has no assignee, queue, SLA or ticket and no phone number. Opt-in per group:
-- a group only has messages stored while `enabled` is true; anything else is dropped by the
-- webhook before persistence. Tenant-owned, FORCE RLS like every other tenant table.
CREATE TABLE wa_groups (
  id                    UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id             UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  channel_connection_id UUID NOT NULL,
  provider_group_id     TEXT NOT NULL,
  name                  TEXT NOT NULL DEFAULT '',
  enabled               BOOLEAN NOT NULL DEFAULT false,
  enabled_by            UUID REFERENCES users(id) ON DELETE SET NULL,
  enabled_at            TIMESTAMPTZ,
  last_message_at       TIMESTAMPTZ,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT wa_groups_tenant_id_uq UNIQUE (tenant_id, id),
  CONSTRAINT wa_groups_provider_uq UNIQUE (tenant_id, channel_connection_id, provider_group_id),
  CONSTRAINT wa_groups_provider_id_format CHECK (provider_group_id ~ '^[0-9]{5,20}(-[0-9]{1,20})?@g\.us$'),
  CONSTRAINT wa_groups_name_len CHECK (char_length(name) <= 200),
  -- history is protected: a connection with stored groups cannot be deleted from under them
  CONSTRAINT wa_groups_connection_fk FOREIGN KEY (tenant_id, channel_connection_id)
    REFERENCES channel_connections (tenant_id, id) ON DELETE RESTRICT
);
CREATE INDEX idx_wa_groups_tenant_activity ON wa_groups (tenant_id, last_message_at DESC NULLS LAST, id) WHERE enabled;

CREATE TABLE wa_group_messages (
  id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id           UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  group_id            UUID NOT NULL,
  provider_message_id TEXT NOT NULL,
  -- the participant who wrote it: an opaque WhatsApp id (often @lid), never a phone number
  author_jid          TEXT NOT NULL DEFAULT '',
  author_name         TEXT NOT NULL DEFAULT '',
  from_me             BOOLEAN NOT NULL DEFAULT false,
  message_type        TEXT NOT NULL DEFAULT 'text',
  body                TEXT NOT NULL DEFAULT '',
  sent_at             TIMESTAMPTZ NOT NULL,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT wa_group_messages_group_fk FOREIGN KEY (tenant_id, group_id)
    REFERENCES wa_groups (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT wa_group_messages_provider_uq UNIQUE (tenant_id, group_id, provider_message_id),
  CONSTRAINT wa_group_messages_provider_id_len CHECK (char_length(provider_message_id) BETWEEN 1 AND 300),
  CONSTRAINT wa_group_messages_author_len CHECK (char_length(author_jid) <= 100 AND char_length(author_name) <= 200),
  CONSTRAINT wa_group_messages_type CHECK (message_type IN ('text','image','video','audio','document','sticker','location','other')),
  CONSTRAINT wa_group_messages_body_len CHECK (char_length(body) <= 20000)
);
CREATE INDEX idx_wa_group_messages_page ON wa_group_messages (tenant_id, group_id, sent_at DESC, id DESC);

ALTER TABLE wa_groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE wa_groups FORCE ROW LEVEL SECURITY;
CREATE POLICY wa_groups_read_tenant ON wa_groups
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_groups_insert_tenant ON wa_groups
  FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_groups_update_tenant ON wa_groups
  FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_groups_delete_tenant ON wa_groups
  FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());

ALTER TABLE wa_group_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE wa_group_messages FORCE ROW LEVEL SECURITY;
CREATE POLICY wa_group_messages_read_tenant ON wa_group_messages
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_messages_insert_tenant ON wa_group_messages
  FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_messages_update_tenant ON wa_group_messages
  FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_messages_delete_tenant ON wa_group_messages
  FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());

GRANT SELECT, INSERT, UPDATE, DELETE ON wa_groups TO omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON wa_group_messages TO omnira_app;
