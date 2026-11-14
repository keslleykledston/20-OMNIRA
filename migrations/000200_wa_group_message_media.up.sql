-- Group chat image attachments (ADR-0015 addendum 2 / ADR-0016 extension): group messages did not store any
-- media reference at all ("mídia fica fora das primeiras fatias", ADR-0015 decision 8) — this is the storage
-- half of lifting that. Mirrors messages/message_media exactly (same columns, same trigger-enqueue pattern,
-- same status machine) so the EXISTING fetch/antivirus pipeline (internal/media/application.Processor) runs
-- unmodified against this new table through a parallel repository — a "fila própria" like ADR-0055 already
-- built for the group AI observer, never touching the 1:1 messages/message_media tables.
ALTER TABLE wa_group_messages
  ADD COLUMN media_ref TEXT NOT NULL DEFAULT '',
  ADD COLUMN mime_type TEXT NOT NULL DEFAULT '';

ALTER TABLE wa_group_messages
  ADD CONSTRAINT wa_group_messages_media_ref_len CHECK (char_length(media_ref) <= 2000),
  ADD CONSTRAINT wa_group_messages_mime_type_len CHECK (char_length(mime_type) <= 100);

CREATE TABLE wa_group_message_media (
  id               UUID NOT NULL DEFAULT uuid_generate_v4(),
  tenant_id        UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  group_message_id UUID NOT NULL,
  status           TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status = ANY (ARRAY['pending', 'quarantined', 'clean', 'infected', 'rejected', 'source_gone', 'failed'])),
  attempts         INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  kind             TEXT NOT NULL DEFAULT '',
  mime             TEXT NOT NULL DEFAULT '',
  size_bytes       BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
  sha256           TEXT NOT NULL DEFAULT '',
  reason           TEXT NOT NULL DEFAULT '',
  fetched_at       TIMESTAMPTZ,
  scanned_at       TIMESTAMPTZ,
  file_purged_at   TIMESTAMPTZ,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (id),
  UNIQUE (tenant_id, group_message_id),
  FOREIGN KEY (tenant_id, group_message_id) REFERENCES wa_group_messages (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_wa_group_message_media_work ON wa_group_message_media (next_attempt_at) WHERE status IN ('pending', 'quarantined');
CREATE INDEX idx_wa_group_message_media_retention ON wa_group_message_media (created_at) WHERE file_purged_at IS NULL AND status IN ('clean', 'quarantined', 'failed');

ALTER TABLE wa_group_message_media ENABLE ROW LEVEL SECURITY;
ALTER TABLE wa_group_message_media FORCE ROW LEVEL SECURITY;

-- Same simple (member OR system) pattern wa_group_messages itself already uses — groups have no Hub-delegated
-- read path into message content, unlike the 1:1 message_media's "delegated" policy.
CREATE POLICY wa_group_message_media_read_tenant ON wa_group_message_media FOR SELECT
  USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_message_media_insert_tenant ON wa_group_message_media FOR INSERT
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
-- UPDATE/DELETE: only the system session (the worker) ever changes a verdict, same as message_media.
CREATE POLICY wa_group_message_media_update_system ON wa_group_message_media FOR UPDATE
  USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY wa_group_message_media_delete_system ON wa_group_message_media FOR DELETE
  USING (is_system_admin());

CREATE OR REPLACE FUNCTION wa_group_message_media_enqueue() RETURNS TRIGGER
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  INSERT INTO wa_group_message_media (tenant_id, group_message_id) VALUES (NEW.tenant_id, NEW.id)
  ON CONFLICT (tenant_id, group_message_id) DO NOTHING;
  RETURN NEW;
END;
$$;

CREATE TRIGGER wa_group_messages_media_enqueue AFTER INSERT ON wa_group_messages
  FOR EACH ROW WHEN (NEW.media_ref <> '') EXECUTE FUNCTION wa_group_message_media_enqueue();
