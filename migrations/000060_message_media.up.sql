-- ADR-0016 M1: inbound media is captured, quarantined, scanned and only then served. One row per
-- inbound message that carries media. The bytes live on disk (never in the database); this row is
-- the state machine and the audit trail. Tenant-owned, FORCE RLS like every other tenant table.
--
--   pending      -> created with the message; the worker still has to fetch the file from WAHA
--                   (WAHA deletes its copy within minutes, so this is time-critical)
--   quarantined  -> fetched, hashed, type-checked and stored in quarantine; antivirus pending
--   clean        -> scanned clean; the only state from which bytes are ever served
--   infected     -> antivirus verdict; bytes deleted, signature + sha256 kept as evidence
--   rejected     -> type not on the allow-list / active content / limits; bytes deleted
--   source_gone  -> WAHA no longer has the file (also the backfill state for media received before M1)
--   failed       -> gave up after retries (bytes, if any, stay in quarantine and are never served)
CREATE TABLE message_media (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  message_id      UUID NOT NULL,
  status          TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','quarantined','clean','infected','rejected','source_gone','failed')),
  attempts        INT  NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  kind            TEXT NOT NULL DEFAULT '',   -- image|audio|video|document, from the real bytes
  mime            TEXT NOT NULL DEFAULT '',   -- from the real bytes, never the declared type
  size_bytes      BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
  sha256          TEXT NOT NULL DEFAULT '',
  reason          TEXT NOT NULL DEFAULT '',   -- why rejected/failed/source_gone, or the antivirus signature
  fetched_at      TIMESTAMPTZ,
  scanned_at      TIMESTAMPTZ,
  file_purged_at  TIMESTAMPTZ,                -- the file was removed by retention; derived text stays
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, message_id),
  FOREIGN KEY (tenant_id, message_id) REFERENCES messages(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX message_media_work_idx ON message_media (next_attempt_at) WHERE status IN ('pending','quarantined');
CREATE INDEX message_media_retention_idx ON message_media (created_at) WHERE file_purged_at IS NULL AND status IN ('clean','quarantined','failed');

ALTER TABLE message_media ENABLE ROW LEVEL SECURITY;
ALTER TABLE message_media FORCE ROW LEVEL SECURITY;
CREATE POLICY message_media_read_tenant ON message_media
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY message_media_insert_tenant ON message_media
  FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY message_media_update_tenant ON message_media
  FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY message_media_delete_tenant ON message_media
  FOR DELETE USING (is_system_admin());
-- Operators only read this table. Writing a verdict is the worker's job (system session), so a
-- compromised operator session cannot mark an infected file "clean".
GRANT SELECT, INSERT, UPDATE, DELETE ON message_media TO omnira_app;

-- Same transaction as the message: no inbound media can exist without its pending row.
CREATE OR REPLACE FUNCTION message_media_enqueue() RETURNS trigger AS $$
BEGIN
  INSERT INTO message_media (tenant_id, message_id) VALUES (NEW.tenant_id, NEW.id)
  ON CONFLICT (tenant_id, message_id) DO NOTHING;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = public;

CREATE TRIGGER messages_media_enqueue
  AFTER INSERT ON messages
  FOR EACH ROW WHEN (NEW.direction = 'inbound' AND NEW.media_ref <> '')
  EXECUTE FUNCTION message_media_enqueue();

-- The open thread refetches on any event for its conversation, so a media verdict (analysing -> clean /
-- blocked) reaches the operator without a reload. Only references travel, never the file or its name.
CREATE OR REPLACE FUNCTION message_media_realtime() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
BEGIN
  IF NEW.status IS DISTINCT FROM OLD.status THEN
    PERFORM realtime_emit(NEW.tenant_id, 'message_status', m.conversation_id,
      jsonb_build_object('message_id', NEW.message_id, 'media_status', NEW.status))
    FROM messages m WHERE m.tenant_id = NEW.tenant_id AND m.id = NEW.message_id;
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER message_media_realtime_trg AFTER UPDATE OF status ON message_media
  FOR EACH ROW EXECUTE FUNCTION message_media_realtime();

-- Media received before this migration was never stored and WAHA has already deleted it.
INSERT INTO message_media (tenant_id, message_id, status, reason, next_attempt_at)
SELECT tenant_id, id, 'source_gone', 'received_before_capture', now()
FROM messages WHERE direction = 'inbound' AND media_ref <> ''
ON CONFLICT (tenant_id, message_id) DO NOTHING;
