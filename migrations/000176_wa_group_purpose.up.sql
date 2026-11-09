-- ADR-0015 addendum: manual work-purpose tag for a WhatsApp group, chosen by an administrator.
-- Distinct from conversation_kind (migration 000076), which is AUTOMATIC and derived from who writes in the
-- group (internal/customer/other/unclassified participants). purpose is never guessed from participants; it
-- is set explicitly, one group at a time, so routing/training/reporting can target one business function:
--   client_management    contract and administrative matters of a client
--   client_support       support/ticketing for a client's own end customers
--   internal_management  internal company management matters
--   internal_team        internal team discussion/information exchange
--   other                default catch-all until someone tags the group
ALTER TABLE wa_groups ADD COLUMN purpose TEXT NOT NULL DEFAULT 'other'
  CHECK (purpose IN ('client_management','client_support','internal_management','internal_team','other'));
ALTER TABLE wa_groups ADD COLUMN purpose_set_by UUID REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE wa_groups ADD COLUMN purpose_set_at TIMESTAMPTZ;

-- Supports "list the client-support groups" style filters without a sequential scan.
CREATE INDEX idx_wa_groups_tenant_purpose ON wa_groups (tenant_id, purpose) WHERE enabled;
