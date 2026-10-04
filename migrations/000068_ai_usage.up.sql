-- ADR-0017 Wave 10: one row per external AI call, success or not. It feeds the per-tenant monthly budget of the tenant's own
-- provider key (Gemini) and gives administrators visibility of what every AI feature costs. Append-only: nothing updates or
-- deletes a row, and only the system (the worker / API acting as the system) can write.
CREATE TABLE ai_usage (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  provider      TEXT NOT NULL CHECK (char_length(provider) BETWEEN 1 AND 40),
  model         TEXT NOT NULL DEFAULT '' CHECK (char_length(model) <= 100),
  task          TEXT NOT NULL CHECK (char_length(task) BETWEEN 1 AND 60),
  input_tokens  INT NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
  output_tokens INT NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
  -- US$ estimate; NULL when the provider is paid by the platform and no price is known (tokens are still recorded)
  cost_usd      NUMERIC(12,6) CHECK (cost_usd IS NULL OR cost_usd >= 0),
  success       BOOLEAN NOT NULL,
  reason        TEXT NOT NULL DEFAULT '' CHECK (char_length(reason) <= 200),
  ref_id        UUID,                                  -- the analysis / decision / summary this call served (no FK: it may be purged)
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ai_usage_tenant_month_idx ON ai_usage (tenant_id, provider, created_at DESC);

ALTER TABLE ai_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_usage FORCE ROW LEVEL SECURITY;
CREATE POLICY ai_usage_read ON ai_usage FOR SELECT USING (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY ai_usage_insert ON ai_usage FOR INSERT WITH CHECK (is_system_admin());
-- no UPDATE / DELETE policy: the ledger is append-only
GRANT SELECT, INSERT ON ai_usage TO omnira_app;
