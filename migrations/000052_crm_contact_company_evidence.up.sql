-- PRODUCT.7B2B: durable, tenant-safe evidence that an OMNIRA Contact has
-- been associated with an external CRM/ERP company. This is deliberately
-- NOT a CRM contact binding (no external_contact_id here — that is
-- PRODUCT.7B2C) and NOT a live mirror of provider state (no cached
-- company name/CNPJ/active — those are fetched from CompanyDirectory when
-- needed, never trusted as durable truth here).
--
-- Source of truth for V1: a successful, server-validated external ticket
-- creation (PRODUCT.6-K2's CreateExternalTicket reaching OutcomeCreated or
-- OutcomeReplaySuccess). That proves: authenticated tenant, authorized
-- conversation action, an operator-selected company that existed uniquely
-- and active in this tenant's real CompanyDirectory, and a provider that
-- accepted the external create. It does NOT prove a CRM contact exists,
-- that the association is exclusive, or that it remains valid forever.
--
-- provider is deliberately NOT a column here: channel_connections.provider
-- is immutable after creation (see internal/channels/adapters/postgres.go
-- Update's own comment — only status/capabilities/secret_ref/session_ref/
-- risk fields/external_account_id are mutable), so connection_id alone
-- already uniquely and durably qualifies the provider/workspace. Storing
-- provider as a second column here would be redundant and could only ever
-- drift from connection_id with no constraint able to prevent it.
--
-- Cardinality: one Contact -> many Companies (no uniqueness forces one
-- company per contact). One (tenant, contact, connection, company) fact
-- can exist at most once ACTIVE at a time (partial unique index below);
-- revoking it and later re-observing the same fact inserts a new active
-- row rather than reopening the old one — evidence is append-only
-- history, never silently rewritten.
CREATE TABLE crm_contact_company_evidence (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  contact_id UUID NOT NULL,
  connection_id UUID NOT NULL,
  external_company_id TEXT NOT NULL,
  -- V1 records only ticket_selection. Extensible later (operator_
  -- confirmation, provider_verified, import) with a small migration when
  -- a real producer exists — never added speculatively.
  source TEXT NOT NULL CHECK (source IN ('ticket_selection')),
  -- FIRST-confirmation provenance only. A repeated trusted observation of
  -- the same active row advances last_verified_at but never rewrites who/
  -- what first established it (see last_verified_at below and the
  -- application-layer upsert). V1 accepts that the latest verifier of a
  -- long-lived active row is therefore not reconstructable from this
  -- table alone.
  actor_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  origin_ticket_id UUID,
  origin_conversation_id UUID,
  -- first_verified_at: set once, at INSERT, never rewritten.
  first_verified_at TIMESTAMPTZ NOT NULL,
  -- last_verified_at: set at INSERT, advanced on every later trusted
  -- observation of the SAME active row. Distinct from updated_at, which
  -- is the generic "row last touched" timestamp (also advanced by
  -- revocation, unrelated to a new trusted observation).
  last_verified_at TIMESTAMPTZ NOT NULL,
  -- NULL = active (current belief). Once set, the row is permanent
  -- history: never reopened, never cleared. A later legitimate
  -- re-verification of the same fact inserts a NEW active row instead
  -- (see the partial unique index below).
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  -- Historical safety: no production code path hard-deletes a contact
  -- today (audited: zero DELETE FROM contacts in the codebase) or a
  -- channel connection (zero DELETE FROM channel_connections) — RESTRICT
  -- never blocks a real operation today, and forces an explicit decision
  -- rather than silently discarding evidence of which contact/workspace a
  -- fact belongs to if such a delete path is ever introduced.
  FOREIGN KEY (tenant_id, contact_id) REFERENCES contacts(tenant_id, id) ON DELETE RESTRICT,
  FOREIGN KEY (tenant_id, connection_id) REFERENCES channel_connections(tenant_id, id) ON DELETE RESTRICT,
  -- Provenance-only references: deleting the origin ticket/conversation
  -- must never delete or corrupt the evidence fact itself, and must never
  -- null out tenant_id (PostgreSQL 16 column-list ON DELETE SET NULL,
  -- verified against the running database before writing this migration).
  FOREIGN KEY (tenant_id, origin_ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE SET NULL (origin_ticket_id),
  FOREIGN KEY (tenant_id, origin_conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE SET NULL (origin_conversation_id)
);

-- The core integrity rule: at most one ACTIVE fact per
-- (tenant, contact, connection, company). Partial unique index, same
-- established convention as conversations_open_contact_channel_uq /
-- tickets_active_conversation_uq / queues_one_default_per_tenant — never
-- a plain UNIQUE, since a revoked historical row must never conflict with
-- a later legitimate re-verification.
CREATE UNIQUE INDEX crm_contact_company_evidence_active_uq
  ON crm_contact_company_evidence (tenant_id, contact_id, connection_id, external_company_id)
  WHERE revoked_at IS NULL;

CREATE INDEX idx_crm_contact_company_evidence_tenant_contact
  ON crm_contact_company_evidence (tenant_id, contact_id)
  WHERE revoked_at IS NULL;

ALTER TABLE crm_contact_company_evidence ENABLE ROW LEVEL SECURITY;
ALTER TABLE crm_contact_company_evidence FORCE ROW LEVEL SECURITY;
CREATE POLICY crm_contact_company_evidence_read_tenant ON crm_contact_company_evidence
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY crm_contact_company_evidence_insert_tenant ON crm_contact_company_evidence
  FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY crm_contact_company_evidence_update_tenant ON crm_contact_company_evidence
  FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY crm_contact_company_evidence_delete_tenant ON crm_contact_company_evidence
  FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON crm_contact_company_evidence TO omnira_app;
