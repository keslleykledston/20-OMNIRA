-- The vision step's output for a group image (ADR-0015 addendum 2 / ADR-0016 extension), mirroring
-- message_media_analysis minus the full-text search column (no group message search feature exists). A
-- parallel repository (internal/groups/adapters) runs the SAME internal/media/application.VisionProcessor
-- against this table and wa_group_message_media, reusing the tenant's own Gemini key/budget (ADR-0016) — the
-- exact same opt-in gate 1:1 vision already uses, never a new consent mechanism.
CREATE TABLE wa_group_message_analysis (
  id               UUID NOT NULL DEFAULT uuid_generate_v4(),
  tenant_id        UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  group_message_id UUID NOT NULL,
  kind             TEXT NOT NULL CHECK (kind = ANY (ARRAY['description', 'document_text'])),
  status           TEXT NOT NULL DEFAULT 'pending' CHECK (status = ANY (ARRAY['pending', 'done', 'empty', 'failed', 'skipped'])),
  engine           TEXT NOT NULL DEFAULT '',
  model            TEXT NOT NULL DEFAULT '',
  language         TEXT NOT NULL DEFAULT '',
  body             TEXT NOT NULL DEFAULT '' CHECK (char_length(body) <= 20000),
  suspicious        BOOLEAN NOT NULL DEFAULT FALSE,
  reason           TEXT NOT NULL DEFAULT '',
  attempts         INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (id),
  UNIQUE (tenant_id, group_message_id, kind),
  FOREIGN KEY (tenant_id, group_message_id) REFERENCES wa_group_messages (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_wa_group_message_analysis_work ON wa_group_message_analysis (next_attempt_at) WHERE status = 'pending';

ALTER TABLE wa_group_message_analysis ENABLE ROW LEVEL SECURITY;
ALTER TABLE wa_group_message_analysis FORCE ROW LEVEL SECURITY;

CREATE POLICY wa_group_message_analysis_read_tenant ON wa_group_message_analysis FOR SELECT
  USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_message_analysis_insert_system ON wa_group_message_analysis FOR INSERT
  WITH CHECK (is_system_admin());
CREATE POLICY wa_group_message_analysis_update_system ON wa_group_message_analysis FOR UPDATE
  USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY wa_group_message_analysis_delete_system ON wa_group_message_analysis FOR DELETE
  USING (is_system_admin());
