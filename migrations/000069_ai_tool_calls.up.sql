-- ADR-0017 Wave 12: audit trail and approval queue of the policy-gated AI tool gateway. The AI never executes anything on its
-- own authority: read tools run under the requesting user's permissions, writes wait here for a person to approve. One row per
-- request; "running" only exists inside the request that executes it (it rolls back with it). (tenant_id, idempotency_key) makes a retry return the first outcome instead of acting twice.
CREATE TABLE ai_tool_calls (
  id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id        UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  topic_thread_id  UUID NOT NULL,
  tool             TEXT NOT NULL CHECK (tool ~ '^[a-z]+(\.[a-z_]+)+$' AND char_length(tool) <= 60),
  risk             TEXT NOT NULL CHECK (risk IN ('read','low_write')),
  source           TEXT NOT NULL CHECK (source IN ('ai','agent')),
  status           TEXT NOT NULL CHECK (status IN ('running','executed','pending_approval','rejected','denied','failed')),
  args             JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (pg_column_size(args) <= 4096),
  result           JSONB CHECK (result IS NULL OR pg_column_size(result) <= 16384),
  error            TEXT NOT NULL DEFAULT '' CHECK (char_length(error) <= 300),
  idempotency_key  TEXT NOT NULL CHECK (char_length(idempotency_key) BETWEEN 8 AND 64),
  requested_by     UUID REFERENCES users(id) ON DELETE SET NULL,
  decided_by       UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at       TIMESTAMPTZ,
  executed_at      TIMESTAMPTZ,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (status NOT IN ('rejected') OR decided_at IS NOT NULL),
  CHECK (status <> 'executed' OR executed_at IS NOT NULL),
  FOREIGN KEY (tenant_id, topic_thread_id) REFERENCES topic_threads(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX ai_tool_calls_topic_idx ON ai_tool_calls (tenant_id, topic_thread_id, created_at DESC);
CREATE INDEX ai_tool_calls_pending_idx ON ai_tool_calls (tenant_id, created_at) WHERE status = 'pending_approval';

ALTER TABLE ai_tool_calls ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_tool_calls FORCE ROW LEVEL SECURITY;
CREATE POLICY ai_tool_calls_read ON ai_tool_calls FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY ai_tool_calls_insert ON ai_tool_calls FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY ai_tool_calls_update ON ai_tool_calls FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
-- no DELETE policy: the trail is permanent
GRANT SELECT, INSERT, UPDATE ON ai_tool_calls TO omnira_app;
