-- ADR-0055: one AI-suggested classification per group message, never applied automatically. Own table, own
-- queue, apart from ADR-0017's intelligence_jobs/routing_decisions on purpose (see ADR-0055 "Alternativas") —
-- this never feeds that pipeline and that pipeline never writes here.
CREATE TABLE wa_group_message_suggestions (
  id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id        UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  group_message_id UUID NOT NULL,
  category         TEXT NOT NULL,
  summary          TEXT NOT NULL DEFAULT '',
  confidence       NUMERIC(3,2),
  model_provider   TEXT NOT NULL DEFAULT '',
  model_name       TEXT NOT NULL DEFAULT '',
  latency_ms       INT,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT wa_group_message_suggestions_message_fk FOREIGN KEY (tenant_id, group_message_id)
    REFERENCES wa_group_messages (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT wa_group_message_suggestions_message_uq UNIQUE (tenant_id, group_message_id),
  CONSTRAINT wa_group_message_suggestions_category CHECK (category IN ('request','complaint','question','info','other')),
  CONSTRAINT wa_group_message_suggestions_summary_len CHECK (char_length(summary) <= 300),
  CONSTRAINT wa_group_message_suggestions_confidence CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1))
);
-- the worker's claim query looks up "does this message already have a suggestion" by group, newest messages first
CREATE INDEX idx_wa_group_message_suggestions_tenant ON wa_group_message_suggestions (tenant_id, group_message_id);

ALTER TABLE wa_group_message_suggestions ENABLE ROW LEVEL SECURITY;
ALTER TABLE wa_group_message_suggestions FORCE ROW LEVEL SECURITY;
CREATE POLICY wa_group_message_suggestions_read_tenant ON wa_group_message_suggestions
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_message_suggestions_insert_tenant ON wa_group_message_suggestions
  FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_message_suggestions_update_tenant ON wa_group_message_suggestions
  FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_message_suggestions_delete_tenant ON wa_group_message_suggestions
  FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());

GRANT SELECT, INSERT, UPDATE, DELETE ON wa_group_message_suggestions TO omnira_app;
