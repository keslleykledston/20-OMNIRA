-- PRODUCT.6-K1: durable safety foundation for external (ERP) ticket
-- creation. No application service exists yet that writes to this table —
-- this migration only lands the durable state a future CreateExternalTicket
-- application service will require before it can safely call
-- TicketingConnector.CreateTicket.
--
-- Why this cannot reuse the messages idempotency pattern (internal/messages/
-- adapters/postgres.go): that pattern's uniqueness lives on the messages
-- table itself (UNIQUE(tenant_id, sent_by_user_id, idempotency_key)) and is
-- scoped per-sender. External ticket creation has a different duplicate
-- risk — the same operation replayed by a different request context (retry
-- from a different process, a different agent picking up a stuck
-- conversation) must still be recognized as the same attempt — so the
-- uniqueness boundary here is tenant-wide: UNIQUE(tenant_id, idempotency_key).
--
-- State model (PRODUCT.6-K1 spec section 5):
--   in_flight         — a provider POST may already have been sent; NOT
--                        automatically safe to retry, ever, even after a
--                        crash or long delay (section 7: crash window).
--   confirmed_success — provider success was observed; external_ticket_id
--                        is known and must never be overwritten by a second
--                        external ID.
--   confirmed_failure — provider definitively rejected the request; no
--                        external ticket is known to exist.
--   outcome_unknown   — a write may have occurred but OMNIRA cannot prove
--                        success or failure (timeout/EOF/5xx/malformed 2xx).
--                        Never automatically becomes in_flight again.
--
-- projection_synced_at is deliberately separate from state: provider success
-- and local ticket projection are different facts (section 6). A row can be
-- confirmed_success with projection_synced_at still NULL if the local
-- enrichment write failed after a successful provider create — that
-- external success evidence must remain durable and visible for
-- reconciliation without ever causing a second POST.
CREATE TABLE ticket_external_create_attempts (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id UUID NOT NULL,
  actor_user_id UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  idempotency_key TEXT NOT NULL CHECK (idempotency_key ~ '^[A-Za-z0-9._:-]{8,128}$'),
  request_hash TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'in_flight'
    CHECK (state IN ('in_flight', 'confirmed_success', 'confirmed_failure', 'outcome_unknown')),
  provider TEXT,
  external_ticket_id TEXT,
  local_ticket_id UUID,
  projection_synced_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- external identity, once recorded, is immutable for the life of the row
  -- (guarded again at the UPDATE statement level — this CHECK alone cannot
  -- express "never changes", only "always present together").
  CHECK (state <> 'confirmed_success' OR (provider IS NOT NULL AND external_ticket_id IS NOT NULL)),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, local_ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE SET NULL
);

CREATE INDEX idx_ticket_external_create_attempts_tenant_conversation
  ON ticket_external_create_attempts(tenant_id, conversation_id);

-- Reconciliation/ops query: find rows a human needs to look at.
CREATE INDEX idx_ticket_external_create_attempts_needs_reconciliation
  ON ticket_external_create_attempts(tenant_id, state, updated_at)
  WHERE state IN ('in_flight', 'outcome_unknown');

ALTER TABLE ticket_external_create_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE ticket_external_create_attempts FORCE ROW LEVEL SECURITY;
CREATE POLICY ticket_external_create_attempts_read_tenant ON ticket_external_create_attempts
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY ticket_external_create_attempts_insert_tenant ON ticket_external_create_attempts
  FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY ticket_external_create_attempts_update_tenant ON ticket_external_create_attempts
  FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY ticket_external_create_attempts_delete_tenant ON ticket_external_create_attempts
  FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON ticket_external_create_attempts TO omnira_app;
