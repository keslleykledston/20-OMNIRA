-- ADR-0038 phase 1: per-company capabilities ("entitlements") switched on and off by the platform control plane, and the
-- idempotency ledger of company creation.
--
-- tenant_entitlements: ABSENCE OF A ROW MEANS ENABLED. Every company that exists today therefore keeps everything it has;
-- a row exists only once an operator has decided. The known capability keys live in Go (internal/entitlements) and are
-- validated there; the table only constrains their shape. Tenant members may READ their own company's switches (the
-- server enforces them, the UI only reflects them); only a system session (the control plane) writes.
CREATE TABLE tenant_entitlements (
  tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  capability  TEXT NOT NULL CHECK (capability ~ '^[a-z][a-z0-9_]{1,63}$'),
  enabled     BOOLEAN NOT NULL,
  updated_by  UUID REFERENCES users(id) ON DELETE SET NULL,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, capability)
);

ALTER TABLE tenant_entitlements ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_entitlements FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_entitlements_read ON tenant_entitlements FOR SELECT
  USING (is_system_admin() OR has_active_membership(tenant_id, current_user_id()));
CREATE POLICY tenant_entitlements_insert ON tenant_entitlements FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY tenant_entitlements_update ON tenant_entitlements FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY tenant_entitlements_delete ON tenant_entitlements FOR DELETE USING (is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_entitlements TO omnira_app;

-- One row per (operator, Idempotency-Key): a retried "create company" returns the company it already created instead of a
-- second one, and the same key with a different body is refused.
CREATE TABLE company_creation_requests (
  operator_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  idempotency_key    TEXT NOT NULL CHECK (char_length(idempotency_key) BETWEEN 8 AND 128),
  request_hash       TEXT NOT NULL,
  created_tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (operator_id, idempotency_key)
);

ALTER TABLE company_creation_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE company_creation_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY company_creation_requests_read ON company_creation_requests FOR SELECT
  USING (is_system_admin() OR operator_id = current_user_id());
CREATE POLICY company_creation_requests_insert ON company_creation_requests FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY company_creation_requests_update ON company_creation_requests FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY company_creation_requests_delete ON company_creation_requests FOR DELETE USING (is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON company_creation_requests TO omnira_app;
