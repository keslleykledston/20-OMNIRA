-- ADR-0050 (M2): importing a conversation export into a group's memory. The raw file is kept ENCRYPTED and IMMUTABLE for a bounded time
-- (90 days by default) so the extraction can be redone when the prompt improves, and is discarded by a mechanical rule. It is NOT stored in
-- wa_group_messages: it stays outside the hot/cold retention of ADR-0015 and is only ever read by the extraction job.

CREATE TABLE wa_group_imports (
  id                    UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id             UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  group_id              UUID NOT NULL,
  status                TEXT NOT NULL DEFAULT 'uploaded' CHECK (status IN ('uploaded','extracting','extracted','failed','discarded')),
  format                TEXT NOT NULL CHECK (format IN ('whatsapp_txt','whatsapp_zip')),
  source_filename       TEXT NOT NULL DEFAULT '' CHECK (char_length(source_filename) <= 200),
  sha256                TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  size_bytes            BIGINT NOT NULL CHECK (size_bytes BETWEEN 1 AND 52428800),
  imported_by           UUID REFERENCES users(id) ON DELETE SET NULL,
  -- the coverage record (ADR-0050 §6): "everything was READ", never "everything was captured"
  parser_version        TEXT NOT NULL DEFAULT '' CHECK (char_length(parser_version) <= 40),
  msgs_total            INTEGER NOT NULL DEFAULT 0 CHECK (msgs_total >= 0),
  msgs_parsed           INTEGER NOT NULL DEFAULT 0 CHECK (msgs_parsed >= 0),
  msgs_rejected         INTEGER NOT NULL DEFAULT 0 CHECK (msgs_rejected >= 0),
  msgs_duplicate        INTEGER NOT NULL DEFAULT 0 CHECK (msgs_duplicate >= 0),
  rejected_reasons      JSONB NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(rejected_reasons) = 'object' AND pg_column_size(rejected_reasons) <= 2048),
  range_from            TIMESTAMPTZ,
  range_to              TIMESTAMPTZ,
  batches_total         INTEGER NOT NULL DEFAULT 0 CHECK (batches_total >= 0),
  batches_ok            INTEGER NOT NULL DEFAULT 0 CHECK (batches_ok >= 0),
  batches_failed        INTEGER NOT NULL DEFAULT 0 CHECK (batches_failed >= 0),
  items_created         INTEGER NOT NULL DEFAULT 0 CHECK (items_created >= 0),
  prompt_version        TEXT NOT NULL DEFAULT '' CHECK (char_length(prompt_version) <= 40),
  extract_runs          INTEGER NOT NULL DEFAULT 0 CHECK (extract_runs >= 0),
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- when the status last changed: an import stuck in 'extracting' (a crashed worker) is picked up again after a while
  status_changed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  extracted_at          TIMESTAMPTZ,
  expires_at            TIMESTAMPTZ NOT NULL,
  discarded_at          TIMESTAMPTZ,
  discard_reason        TEXT CHECK (discard_reason IS NULL OR discard_reason IN ('confirmed','expired','history_deleted','group_ai_disabled','memory_erased')),
  discard_confirmed_by  UUID REFERENCES users(id) ON DELETE SET NULL,
  version               INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
  CONSTRAINT wa_group_imports_tenant_id_uq UNIQUE (tenant_id, id),
  CONSTRAINT wa_group_imports_group_fk FOREIGN KEY (tenant_id, group_id)
    REFERENCES wa_groups (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT wa_group_imports_discard_consistent CHECK ((status = 'discarded') = (discarded_at IS NOT NULL AND discard_reason IS NOT NULL)),
  CONSTRAINT wa_group_imports_counts_consistent CHECK (msgs_parsed + msgs_rejected <= msgs_total AND batches_ok + batches_failed <= batches_total)
);
-- the same file is never imported twice into the same group
CREATE UNIQUE INDEX wa_group_imports_sha_uq ON wa_group_imports (tenant_id, group_id, sha256) WHERE status <> 'discarded';
CREATE INDEX idx_wa_group_imports_group ON wa_group_imports (tenant_id, group_id, created_at DESC);
-- the expiry sweep
CREATE INDEX idx_wa_group_imports_expiry ON wa_group_imports (expires_at) WHERE status <> 'discarded';

-- The encrypted payloads, apart from the metadata: raw = the file as received, normalized = the parsed messages (JSONL), so redoing the
-- extraction never depends on parsing the export again. Written once (UPDATE is revoked); deleted on discard.
CREATE TABLE wa_group_import_blobs (
  import_id       UUID PRIMARY KEY,
  tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  raw_enc         BYTEA NOT NULL,
  normalized_enc  BYTEA NOT NULL,
  key_version     INTEGER NOT NULL DEFAULT 1 CHECK (key_version >= 1),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT wa_group_import_blobs_import_fk FOREIGN KEY (tenant_id, import_id)
    REFERENCES wa_group_imports (tenant_id, id) ON DELETE CASCADE
);

ALTER TABLE wa_group_imports ENABLE ROW LEVEL SECURITY;
ALTER TABLE wa_group_imports FORCE ROW LEVEL SECURITY;
CREATE POLICY wa_group_imports_read_tenant ON wa_group_imports
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_imports_insert_tenant ON wa_group_imports
  FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_imports_update_tenant ON wa_group_imports
  FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());

ALTER TABLE wa_group_import_blobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE wa_group_import_blobs FORCE ROW LEVEL SECURITY;
CREATE POLICY wa_group_import_blobs_read_tenant ON wa_group_import_blobs
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_import_blobs_insert_tenant ON wa_group_import_blobs
  FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_import_blobs_delete_tenant ON wa_group_import_blobs
  FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());

GRANT SELECT, INSERT, UPDATE ON wa_group_imports TO omnira_app;
REVOKE DELETE ON wa_group_imports FROM omnira_app; -- the metadata (sha256, coverage, discard reason) outlives the payload
GRANT SELECT, INSERT, DELETE ON wa_group_import_blobs TO omnira_app;
REVOKE UPDATE ON wa_group_import_blobs FROM omnira_app; -- immutable once written
CREATE FUNCTION wa_group_import_blobs_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'wa_group_import_blobs are immutable' USING ERRCODE = 'integrity_constraint_violation';
END $$;
CREATE TRIGGER wa_group_import_blobs_no_update BEFORE UPDATE ON wa_group_import_blobs
  FOR EACH ROW EXECUTE FUNCTION wa_group_import_blobs_immutable();
