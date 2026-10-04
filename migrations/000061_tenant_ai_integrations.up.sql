-- ADR-0016: per-tenant opt-in for the external AI provider (Gemini) that reads images and scanned PDFs.
-- OFF by default. A tenant administrator supplies the provider API key (write-only, AES-256-GCM like channel
-- credentials), records an explicit consent, and may then switch it on. Audio never uses this: it is
-- transcribed locally and never leaves the server.
CREATE TABLE tenant_ai_integrations (
  tenant_id          UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  provider           TEXT NOT NULL CHECK (provider IN ('gemini')),
  enabled            BOOLEAN NOT NULL DEFAULT false,
  model              TEXT NOT NULL DEFAULT 'gemini-2.5-flash' CHECK (model ~ '^[A-Za-z0-9._-]{1,100}$'),
  -- Monthly spending cap in US dollars (decision 2: US$ 10 to start). Enforcement happens where calls are made.
  monthly_budget_usd NUMERIC(8,2) NOT NULL DEFAULT 10.00 CHECK (monthly_budget_usd >= 0 AND monthly_budget_usd <= 10000),
  secret_ciphertext  BYTEA,             -- nonce || AES-256-GCM(key); never selected by the API
  secret_set_at      TIMESTAMPTZ,
  secret_set_by      UUID REFERENCES users(id) ON DELETE SET NULL,
  consent_version    TEXT,
  consent_at         TIMESTAMPTZ,
  consent_by         UUID REFERENCES users(id) ON DELETE SET NULL,
  last_test_at       TIMESTAMPTZ,
  last_test_ok       BOOLEAN,
  last_test_message  TEXT NOT NULL DEFAULT '',
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, provider),
  -- It cannot be on without a key and a recorded consent, whatever the application does.
  CONSTRAINT tenant_ai_enabled_requires_key_and_consent
    CHECK (NOT enabled OR (secret_ciphertext IS NOT NULL AND consent_at IS NOT NULL))
);

-- Only an administrator of the tenant (or the system, i.e. the worker) may even see the row: the ciphertext
-- lives here, so ordinary members get nothing at the database level, not just at the API.
ALTER TABLE tenant_ai_integrations ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_ai_integrations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_ai_select ON tenant_ai_integrations
  FOR SELECT USING (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY tenant_ai_insert ON tenant_ai_integrations
  FOR INSERT WITH CHECK (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY tenant_ai_update ON tenant_ai_integrations
  FOR UPDATE USING (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY tenant_ai_delete ON tenant_ai_integrations
  FOR DELETE USING (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_ai_integrations TO omnira_app;
