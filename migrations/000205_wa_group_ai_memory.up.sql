-- ADR-0050 (M0): group copilot memory. The memory is DERIVED data: typed items with their evidence, never the source of truth, and
-- editable by people. Three tables plus the AI opt-in of a group (kept apart from the collection opt-in `enabled`) and four keys.
--
-- Tenant-owned, FORCE RLS like every other tenant table; authorization (group.ai.*) is checked by the handler against the role
-- matrix of an ACTIVE membership, on top of RLS, exactly like the group APIs of 000057/000058.

ALTER TABLE wa_groups
  ADD COLUMN ai_enabled    BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN ai_enabled_by UUID REFERENCES users(id) ON DELETE SET NULL,
  ADD COLUMN ai_enabled_at TIMESTAMPTZ;

CREATE TABLE wa_group_memory_items (
  id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id          UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  group_id           UUID NOT NULL,
  kind               TEXT NOT NULL CHECK (kind IN ('request','handled_topic','fact','agreement','participant_role')),
  -- episodic = recent, with evidence; consolidated = condensed knowledge (no literal quote, people by role)
  tier               TEXT NOT NULL DEFAULT 'episodic' CHECK (tier IN ('episodic','consolidated')),
  status             TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved','superseded')),
  truth_level        TEXT NOT NULL DEFAULT 'ai_inferred' CHECK (truth_level IN ('said_in_group','ai_inferred','human_confirmed')),
  summary            TEXT NOT NULL CHECK (char_length(btrim(summary)) BETWEEN 1 AND 600),
  -- who it is about, as the group shows them (episodic) or by role (consolidated); never a phone number or a JID
  actor_label        TEXT NOT NULL DEFAULT '' CHECK (char_length(actor_label) <= 120),
  -- [{ref, quote, at}]: where it came from. quote <= 200 chars; removed on consolidation and on "delete history"
  evidence           JSONB NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(evidence) = 'array' AND jsonb_array_length(evidence) <= 5),
  pinned             BOOLEAN NOT NULL DEFAULT false,
  -- decays with time, rises when the item is used or confirmed. It only ranks what enters the prompt; it never deletes.
  relevance          REAL NOT NULL DEFAULT 1 CHECK (relevance >= 0 AND relevance <= 10),
  last_reinforced_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  source             TEXT NOT NULL DEFAULT 'live' CHECK (source IN ('live','import','human','consolidation')),
  source_import_id   UUID,
  prompt_version     TEXT NOT NULL DEFAULT '' CHECK (char_length(prompt_version) <= 40),
  superseded_by      UUID,
  -- dedupe key: sha256 of kind + normalized summary, so the same pending request is not stored twice
  fingerprint        TEXT NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
  created_by         UUID REFERENCES users(id) ON DELETE SET NULL,
  version            INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT wa_group_memory_items_tenant_id_uq UNIQUE (tenant_id, id),
  CONSTRAINT wa_group_memory_items_group_fk FOREIGN KEY (tenant_id, group_id)
    REFERENCES wa_groups (tenant_id, id) ON DELETE CASCADE,
  -- a consolidated item never carries a literal quote
  CONSTRAINT wa_group_memory_items_consolidated_no_quote CHECK (tier = 'episodic' OR evidence = '[]'::jsonb)
);
-- the same live fact is stored once per group while it still counts
CREATE UNIQUE INDEX wa_group_memory_items_fingerprint_uq
  ON wa_group_memory_items (tenant_id, group_id, fingerprint) WHERE status <> 'superseded';
-- what the copilot reads: open and recent first
CREATE INDEX idx_wa_group_memory_items_rank
  ON wa_group_memory_items (tenant_id, group_id, status, pinned DESC, relevance DESC, last_reinforced_at DESC)
  WHERE status <> 'superseded';

-- Every change of an item. Content-free on purpose (ids, enums, counts): it survives "delete the group's memory" as the audit
-- trail without being personal data, so there is no foreign key to the item.
CREATE TABLE wa_group_memory_item_events (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  group_id      UUID NOT NULL,
  item_id       UUID,
  event_type    TEXT NOT NULL CHECK (event_type IN ('created','updated','resolved','reopened','confirmed','pinned','unpinned','superseded',
                                                     'consolidated','quotes_purged','deleted','memory_erased')),
  actor_type    TEXT NOT NULL CHECK (actor_type IN ('ai','human','system')),
  actor_user_id UUID,
  payload       JSONB NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(payload) = 'object' AND pg_column_size(payload) <= 2048),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT wa_group_memory_item_events_group_fk FOREIGN KEY (tenant_id, group_id)
    REFERENCES wa_groups (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_wa_group_memory_item_events_item ON wa_group_memory_item_events (tenant_id, group_id, created_at DESC);

-- Where the incremental reading stopped, per group.
CREATE TABLE wa_group_memory_state (
  tenant_id                 UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  group_id                  UUID NOT NULL,
  last_processed_at         TIMESTAMPTZ,
  last_processed_message_id UUID,
  last_consolidated_at      TIMESTAMPTZ,
  last_run_at               TIMESTAMPTZ,
  -- when the worker last looked at the group, whether or not it had anything to do: it makes the scan fair among quiet groups
  last_checked_at           TIMESTAMPTZ,
  last_run_ok               BOOLEAN,
  -- batches in a row whose answer was unusable at the same cursor: after a few the batch is skipped so one bad batch cannot block a group forever
  consecutive_failures      INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  -- asked from the screen: the worker does the reading (a model call is never made inside a request transaction)
  analyze_requested_at      TIMESTAMPTZ,
  analyze_requested_by      UUID REFERENCES users(id) ON DELETE SET NULL,
  consolidate_requested_at  TIMESTAMPTZ,
  prompt_version            TEXT NOT NULL DEFAULT '' CHECK (char_length(prompt_version) <= 40),
  runs                      INTEGER NOT NULL DEFAULT 0 CHECK (runs >= 0),
  input_tokens              BIGINT NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
  output_tokens             BIGINT NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
  -- a per-day budget guard: the day the counters below belong to
  budget_day                DATE,
  budget_calls              INTEGER NOT NULL DEFAULT 0 CHECK (budget_calls >= 0),
  updated_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, group_id),
  CONSTRAINT wa_group_memory_state_group_fk FOREIGN KEY (tenant_id, group_id)
    REFERENCES wa_groups (tenant_id, id) ON DELETE CASCADE
);

ALTER TABLE wa_group_memory_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE wa_group_memory_items FORCE ROW LEVEL SECURITY;
CREATE POLICY wa_group_memory_items_read_tenant ON wa_group_memory_items
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_memory_items_insert_tenant ON wa_group_memory_items
  FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_memory_items_update_tenant ON wa_group_memory_items
  FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_memory_items_delete_tenant ON wa_group_memory_items
  FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());

ALTER TABLE wa_group_memory_item_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE wa_group_memory_item_events FORCE ROW LEVEL SECURITY;
CREATE POLICY wa_group_memory_item_events_read_tenant ON wa_group_memory_item_events
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_memory_item_events_insert_tenant ON wa_group_memory_item_events
  FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());

ALTER TABLE wa_group_memory_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE wa_group_memory_state FORCE ROW LEVEL SECURITY;
CREATE POLICY wa_group_memory_state_read_tenant ON wa_group_memory_state
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_memory_state_insert_tenant ON wa_group_memory_state
  FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_memory_state_update_tenant ON wa_group_memory_state
  FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_memory_state_delete_tenant ON wa_group_memory_state
  FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());

GRANT SELECT, INSERT, UPDATE, DELETE ON wa_group_memory_items TO omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON wa_group_memory_state TO omnira_app;
-- Migration 000006 grants DML on every new table through default privileges, so the immutability is made real here (see 000087):
-- the events are append-only for the application, and a trigger stops even the owner from rewriting one.
GRANT SELECT, INSERT ON wa_group_memory_item_events TO omnira_app;
REVOKE UPDATE, DELETE ON wa_group_memory_item_events FROM omnira_app;
CREATE FUNCTION wa_group_memory_item_events_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'wa_group_memory_item_events are immutable' USING ERRCODE = 'integrity_constraint_violation';
END $$;
CREATE TRIGGER wa_group_memory_item_events_no_update BEFORE UPDATE ON wa_group_memory_item_events
  FOR EACH ROW EXECUTE FUNCTION wa_group_memory_item_events_immutable();

-- Permissions (ADR-0050 §9): same default as group.read/group.manage - admin and supervisor, never the plain agent.
INSERT INTO permissions(key, description) VALUES
  ('group.ai.read',      'Read the memory and the proposals of a group the AI is enabled for'),
  ('group.ai.manage',    'Turn the AI on for a group; edit, confirm, pin and delete memory items; analyze the group; approve or reject proposals'),
  ('group.import',       'Import a conversation export file into a group''s memory'),
  ('group.memory.erase', 'Erase everything the AI remembers about a group')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, p.key FROM roles r CROSS JOIN permissions p
WHERE r.tenant_id IS NULL AND r.key IN ('tenant_admin','tenant_supervisor')
  AND p.key IN ('group.ai.read','group.ai.manage','group.import','group.memory.erase')
ON CONFLICT DO NOTHING;
