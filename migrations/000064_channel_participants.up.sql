-- ADR-0017 Wave 2: external participants of a channel (who wrote a message), distinct from conversation_participants
-- (which are OMNIRA agents co-attending a conversation). A channel participant is identified by the provider's own id
-- (a WhatsApp @lid/@c.us, a Meta wa_id); it is optionally bound to a Contact. Also preserves reply metadata so the
-- router can use a direct reply as strong evidence.

CREATE TABLE channel_participants (
  id                      UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id               UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  channel_connection_id   UUID NOT NULL,
  provider                TEXT NOT NULL CHECK (char_length(provider) BETWEEN 1 AND 40),
  external_participant_id TEXT NOT NULL CHECK (char_length(external_participant_id) BETWEEN 1 AND 100),
  display_name            TEXT NOT NULL DEFAULT '' CHECK (char_length(display_name) <= 200),
  contact_id              UUID,
  created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  -- provider-qualified AND connection-qualified: the same id on another connection or tenant is another participant
  UNIQUE (tenant_id, channel_connection_id, provider, external_participant_id),
  FOREIGN KEY (tenant_id, channel_connection_id) REFERENCES channel_connections(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, contact_id) REFERENCES contacts(tenant_id, id) ON DELETE SET NULL (contact_id)
);
CREATE INDEX channel_participants_contact_idx ON channel_participants (tenant_id, contact_id) WHERE contact_id IS NOT NULL;

CREATE TABLE conversation_channel_participants (
  tenant_id              UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id        UUID NOT NULL,
  channel_participant_id UUID NOT NULL,
  role                   TEXT CHECK (role IS NULL OR role IN ('customer','member','admin','owner')),
  first_seen_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, conversation_id, channel_participant_id),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, channel_participant_id) REFERENCES channel_participants(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX conversation_channel_participants_participant_idx ON conversation_channel_participants (tenant_id, channel_participant_id);

-- Message metadata. reply_to_external_message_id keeps what the provider said even when the quoted message is not in
-- OMNIRA (older than the connection, or not stored); reply_to_message_id is set only when it resolves inside the SAME
-- conversation. WhatsApp's "quoted" and Meta's "context" are the same relation as a reply, so there is one column set.
ALTER TABLE messages
  ADD COLUMN sender_channel_participant_id UUID,
  ADD COLUMN reply_to_message_id UUID,
  ADD COLUMN reply_to_external_message_id TEXT NOT NULL DEFAULT '' CHECK (char_length(reply_to_external_message_id) <= 300),
  ADD FOREIGN KEY (tenant_id, sender_channel_participant_id) REFERENCES channel_participants(tenant_id, id) ON DELETE SET NULL (sender_channel_participant_id),
  ADD FOREIGN KEY (tenant_id, reply_to_message_id) REFERENCES messages(tenant_id, id) ON DELETE SET NULL (reply_to_message_id);
CREATE INDEX messages_reply_to_idx ON messages (tenant_id, reply_to_message_id) WHERE reply_to_message_id IS NOT NULL;

-- Group messages (ADR-0015) get the same, resolved inside the same group.
ALTER TABLE wa_group_messages ADD CONSTRAINT wa_group_messages_tenant_id_uq UNIQUE (tenant_id, id);
ALTER TABLE wa_group_messages
  ADD COLUMN sender_channel_participant_id UUID,
  ADD COLUMN reply_to_group_message_id UUID,
  ADD COLUMN reply_to_external_message_id TEXT NOT NULL DEFAULT '' CHECK (char_length(reply_to_external_message_id) <= 300),
  ADD FOREIGN KEY (tenant_id, sender_channel_participant_id) REFERENCES channel_participants(tenant_id, id) ON DELETE SET NULL (sender_channel_participant_id),
  ADD FOREIGN KEY (tenant_id, reply_to_group_message_id) REFERENCES wa_group_messages(tenant_id, id) ON DELETE SET NULL (reply_to_group_message_id);

DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['channel_participants','conversation_channel_participants'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_read_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_insert_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_update_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_delete_tenant', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO omnira_app', t);
  END LOOP;
END $$;
