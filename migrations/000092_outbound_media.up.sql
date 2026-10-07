-- ADR-0024: files an operator sends to a customer. The bytes live on disk (<media dir>/outbound/<tenant>/<id>, never in this database);
-- this table is their record. A row is born "ready" at upload (the API has already classified the bytes by content and had ClamAV clear
-- them) and is attached to exactly ONE outbound message when the operator sends it. Unsent rows expire (24 h) and are swept by the worker.
CREATE TABLE message_outbound_media (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id UUID NOT NULL,
  uploaded_by     UUID NOT NULL REFERENCES users(id),
  kind            TEXT NOT NULL CHECK (kind IN ('image', 'audio', 'video', 'document')),
  mime            TEXT NOT NULL CHECK (char_length(mime) BETWEEN 3 AND 100),   -- from the real bytes, never the declared type
  size_bytes      BIGINT NOT NULL CHECK (size_bytes > 0),
  sha256          TEXT NOT NULL CHECK (char_length(sha256) = 64),
  file_name       TEXT NOT NULL DEFAULT '' CHECK (char_length(file_name) <= 120),  -- sanitized label for the customer; never a path
  message_id      UUID,                                                       -- set once, when the message that carries it is queued
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at      TIMESTAMPTZ NOT NULL DEFAULT now() + interval '24 hours',   -- only meaningful while message_id IS NULL
  file_purged_at  TIMESTAMPTZ,                                                -- the file was removed (expired unsent, or retention)
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, message_id) REFERENCES messages(tenant_id, id) ON DELETE CASCADE,
  UNIQUE (tenant_id, message_id)
);
CREATE INDEX message_outbound_media_pending_idx ON message_outbound_media (expires_at) WHERE message_id IS NULL AND file_purged_at IS NULL;
CREATE INDEX message_outbound_media_retention_idx ON message_outbound_media (created_at) WHERE message_id IS NOT NULL AND file_purged_at IS NULL;
CREATE INDEX message_outbound_media_conversation_idx ON message_outbound_media (tenant_id, conversation_id, uploaded_by) WHERE message_id IS NULL;

ALTER TABLE message_outbound_media ENABLE ROW LEVEL SECURITY;
ALTER TABLE message_outbound_media FORCE ROW LEVEL SECURITY;
CREATE POLICY message_outbound_media_read_tenant ON message_outbound_media
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
-- An operator can only register an upload as themselves.
CREATE POLICY message_outbound_media_insert_tenant ON message_outbound_media
  FOR INSERT WITH CHECK ((has_active_membership(tenant_id, current_user_id()) AND uploaded_by = current_user_id()) OR is_system_admin());
-- Attaching to a message is the only update an operator can make: their OWN, still unattached upload. The worker (system) does the rest.
CREATE POLICY message_outbound_media_attach_tenant ON message_outbound_media
  FOR UPDATE USING ((has_active_membership(tenant_id, current_user_id()) AND uploaded_by = current_user_id() AND message_id IS NULL) OR is_system_admin())
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY message_outbound_media_delete_system ON message_outbound_media
  FOR DELETE USING (is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON message_outbound_media TO omnira_app;
