-- PRODUCT.6-O2B1: durable safety foundation for external (ERP) ticket
-- STATUS MUTATION. No connector method or application service exists yet
-- that writes to this table — this migration only lands the durable state
-- a future status-mutation application service will require before it can
-- safely call TicketingConnector.UpdateTicketStatus.
--
-- Deliberately a SEPARATE table from ticket_external_create_attempts
-- (PRODUCT.6-K1, migration 000047) — NOT reused — because status mutation
-- has different lifecycle safety semantics (PRODUCT.6-O2A3/O2B1 section 5):
--
--   For CREATE, confirmed_success permanently blocks another external
--   creation for the same local ticket (there can only ever be one create).
--
--   For STATUS MUTATION, confirmed_success and confirmed_failure are BOTH
--   non-blocking: status 2 -> later status 4 -> later status 5 are valid,
--   distinct, sequential operations against the SAME local ticket. Only
--   in_flight and outcome_unknown — genuinely unresolved operations — may
--   block a new mutation attempt from acquiring.
--
-- Confirmed real K3G write contract (PRODUCT.6-O2A3, live evidence against
-- ticket 9115): PUT /api/support/tickets/{id}/status, body {"status":
-- <integer>}, official vocabulary 1 Novo / 2 Em atendimento / 3 Planejado /
-- 4 Pendente / 5 Resolvido / 6 Encerrado. Same-target re-submission is
-- provider-confirmed safe, but that fact must NEVER be used to weaken the
-- unresolved-operation invariant below (PRODUCT.6-O2A3 section 17): the
-- unsafe case is not "5 -> 5", it is ordering — a stale ambiguous mutation
-- for an earlier target replaying after a later, different mutation has
-- already been legitimately applied.
--
-- local_ticket_id, provider, external_ticket_id and target_status are all
-- NOT NULL from row creation (unlike ticket_external_create_attempts,
-- where provider/external_ticket_id are only known once the provider
-- responds): a status mutation attempt only ever targets an ALREADY
-- LINKED ticket whose provider/external_ticket_id are durably known
-- before the provider is ever called (PRODUCT.6-O2A section 9 — linked=
-- false / inconsistent linkage / provider mismatch are all preconditions
-- checked BEFORE an attempt is acquired, exactly like
-- CreateExternalTicket's existing-link guard, PRODUCT.6-M5).
--
-- State model (same AttemptState vocabulary as PRODUCT.6-K1, reused —
-- internal/tickets/domain.AttemptState):
--   in_flight         — a provider PUT may already have been sent; NOT
--                        automatically safe to retry, ever, even after a
--                        crash or long delay. BLOCKING.
--   confirmed_success — provider mutation confirmed/reconciled against the
--                        requested target_status. NON-BLOCKING: a later,
--                        different target_status is a legitimate new
--                        operation, not a duplicate of this one.
--   confirmed_failure — provider definitively rejected the mutation; no
--                        lifecycle change occurred. NON-BLOCKING: a later
--                        corrected intent may proceed.
--   outcome_unknown   — a write may have occurred but OMNIRA cannot prove
--                        the outcome (timeout/5xx/malformed 2xx). Never
--                        automatically becomes in_flight again (no
--                        automatic retry). BLOCKING — a new key must not
--                        acquire while this ambiguity is unresolved,
--                        because the eventual reconciliation of THIS
--                        attempt must not race with, or be silently
--                        superseded by, a different target requested by a
--                        different key.
--
-- confirmed_external_status/confirmed_external_status_label capture the
-- RECONCILED provider snapshot (either the mutation response itself, or a
-- single read-back GetTicket, PRODUCT.6-O2A3 section 9) — kept separate
-- from target_status (what was REQUESTED) so a reconciliation mismatch
-- (confirmed status != target status) remains visible rather than
-- overwriting the original intent.
CREATE TABLE ticket_external_status_attempts (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  local_ticket_id UUID NOT NULL,
  conversation_id UUID NOT NULL,
  actor_user_id UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  idempotency_key TEXT NOT NULL CHECK (idempotency_key ~ '^[A-Za-z0-9._:-]{8,128}$'),
  request_hash TEXT NOT NULL,
  provider TEXT NOT NULL,
  external_ticket_id TEXT NOT NULL,
  target_status TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'in_flight'
    CHECK (state IN ('in_flight', 'confirmed_success', 'confirmed_failure', 'outcome_unknown')),
  confirmed_external_status TEXT,
  confirmed_external_status_label TEXT,
  projection_synced_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- confirmed_success must carry the reconciled snapshot it is confirming.
  CHECK (state <> 'confirmed_success' OR confirmed_external_status IS NOT NULL),
  UNIQUE (tenant_id, id),
  -- Tenant-wide, mirroring ticket_external_create_attempts (000047): the
  -- same operation replayed by a different request context (retry from a
  -- different process, a different agent picking up the conversation)
  -- must still be recognized as the same durable attempt.
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE,
  -- RESTRICT, not SET NULL (unlike 000047's local_ticket_id): this
  -- column is NOT NULL by design — a status attempt without a known
  -- local ticket is meaningless — so the identity it durably records must
  -- never be silently orphaned by a ticket deletion (no ticket deletion
  -- path exists in the codebase today; RESTRICT keeps that true).
  FOREIGN KEY (tenant_id, local_ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE RESTRICT
);

CREATE INDEX idx_ticket_external_status_attempts_tenant_conversation
  ON ticket_external_status_attempts(tenant_id, conversation_id);

-- Reconciliation/ops query: find rows a human needs to look at.
CREATE INDEX idx_ticket_external_status_attempts_needs_reconciliation
  ON ticket_external_status_attempts(tenant_id, state, updated_at)
  WHERE state IN ('in_flight', 'outcome_unknown');

-- The final concurrency arbiter (PRODUCT.6-O2B1 section 6): at most ONE
-- UNRESOLVED (in_flight or outcome_unknown) status mutation attempt may
-- exist per (tenant_id, local_ticket_id) at a time — never a process
-- mutex, never a SELECT-then-INSERT in application code. Unlike
-- ticket_external_create_attempts_blocking_local_ticket_uq (000048),
-- confirmed_success is deliberately NOT in this WHERE clause: a
-- successfully confirmed status mutation must never block the next
-- legitimate lifecycle transition for the same ticket.
CREATE UNIQUE INDEX ticket_external_status_attempts_blocking_local_ticket_uq
  ON ticket_external_status_attempts(tenant_id, local_ticket_id)
  WHERE state IN ('in_flight', 'outcome_unknown');

ALTER TABLE ticket_external_status_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE ticket_external_status_attempts FORCE ROW LEVEL SECURITY;
CREATE POLICY ticket_external_status_attempts_read_tenant ON ticket_external_status_attempts
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY ticket_external_status_attempts_insert_tenant ON ticket_external_status_attempts
  FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY ticket_external_status_attempts_update_tenant ON ticket_external_status_attempts
  FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY ticket_external_status_attempts_delete_tenant ON ticket_external_status_attempts
  FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON ticket_external_status_attempts TO omnira_app;
