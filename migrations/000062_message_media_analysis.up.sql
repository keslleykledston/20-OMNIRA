-- ADR-0016 M2: text derived from an attachment (audio transcript now; image description and PDF text later).
-- It belongs to the MESSAGE, not to the file: when retention deletes the file after 60 days the text stays on the
-- conversation (decision 4). Operators read it; only the worker (system session) writes it, so an operator
-- session cannot plant or alter "AI" text. The body is untrusted data: it is displayed as plain text only and
-- never treated as an instruction.
CREATE TABLE message_media_analysis (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  message_id      UUID NOT NULL,
  kind            TEXT NOT NULL CHECK (kind IN ('transcript','description','document_text')),
  -- pending: waiting for the engine; done: text stored; empty: nothing intelligible (silence, music);
  -- failed: gave up; skipped: not allowed to run (for example the tenant did not opt in to the external AI)
  status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','done','empty','failed','skipped')),
  engine          TEXT NOT NULL DEFAULT '',     -- whisper-local | gemini
  model           TEXT NOT NULL DEFAULT '',
  language        TEXT NOT NULL DEFAULT '',
  body            TEXT NOT NULL DEFAULT '' CHECK (char_length(body) <= 20000),
  suspicious      BOOLEAN NOT NULL DEFAULT false, -- the text looks like it is giving instructions to an AI
  reason          TEXT NOT NULL DEFAULT '',
  attempts        INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  tsv             TSVECTOR GENERATED ALWAYS AS (to_tsvector('portuguese', body)) STORED,
  UNIQUE (tenant_id, message_id, kind),
  FOREIGN KEY (tenant_id, message_id) REFERENCES messages(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX message_media_analysis_work_idx ON message_media_analysis (next_attempt_at) WHERE status = 'pending';
CREATE INDEX message_media_analysis_tsv_idx ON message_media_analysis USING GIN (tsv) WHERE status = 'done';

ALTER TABLE message_media_analysis ENABLE ROW LEVEL SECURITY;
ALTER TABLE message_media_analysis FORCE ROW LEVEL SECURITY;
CREATE POLICY message_media_analysis_read ON message_media_analysis
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY message_media_analysis_insert ON message_media_analysis
  FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY message_media_analysis_update ON message_media_analysis
  FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY message_media_analysis_delete ON message_media_analysis
  FOR DELETE USING (is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON message_media_analysis TO omnira_app;

-- A cleared audio file gets a transcript job (same transaction as the verdict).
CREATE OR REPLACE FUNCTION message_media_analysis_enqueue() RETURNS trigger AS $$
BEGIN
  INSERT INTO message_media_analysis (tenant_id, message_id, kind, engine)
  VALUES (NEW.tenant_id, NEW.message_id, 'transcript', 'whisper-local')
  ON CONFLICT (tenant_id, message_id, kind) DO NOTHING;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = public;

CREATE TRIGGER message_media_analysis_enqueue_trg
  AFTER UPDATE OF status ON message_media
  FOR EACH ROW WHEN (NEW.status = 'clean' AND NEW.kind = 'audio' AND OLD.status IS DISTINCT FROM 'clean')
  EXECUTE FUNCTION message_media_analysis_enqueue();

-- The open thread refetches on any event for its conversation, so the transcript appears without a reload.
CREATE OR REPLACE FUNCTION message_media_analysis_realtime() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
BEGIN
  IF NEW.status IS DISTINCT FROM OLD.status THEN
    PERFORM realtime_emit(NEW.tenant_id, 'message_status', m.conversation_id,
      jsonb_build_object('message_id', NEW.message_id, 'analysis_status', NEW.status))
    FROM messages m WHERE m.tenant_id = NEW.tenant_id AND m.id = NEW.message_id;
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER message_media_analysis_realtime_trg AFTER UPDATE OF status ON message_media_analysis
  FOR EACH ROW EXECUTE FUNCTION message_media_analysis_realtime();

-- Audio already cleared before this migration is transcribed too.
INSERT INTO message_media_analysis (tenant_id, message_id, kind, engine)
SELECT tenant_id, message_id, 'transcript', 'whisper-local'
FROM message_media WHERE status = 'clean' AND kind = 'audio' AND file_purged_at IS NULL
ON CONFLICT (tenant_id, message_id, kind) DO NOTHING;
