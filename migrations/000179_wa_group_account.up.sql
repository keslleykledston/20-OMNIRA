-- Product proposal (Grupos): link a WhatsApp group to the "cliente final" it belongs to, reusing the
-- company registry that already exists for contacts/tickets/topics (ADR-0018's customer_accounts) instead
-- of duplicating a company catalog. The "prestadora" is the tenant itself — no new concept there.
-- One group belongs to at most one account (a client can have several groups; a group never spans two
-- clients). NULL means "not linked yet" — surfaced so an administrator can find and link it.
ALTER TABLE wa_groups ADD COLUMN account_id UUID;
ALTER TABLE wa_groups ADD CONSTRAINT wa_groups_account_fk
  FOREIGN KEY (tenant_id, account_id) REFERENCES customer_accounts(tenant_id, id) ON DELETE RESTRICT;
ALTER TABLE wa_groups ADD COLUMN account_set_by UUID REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE wa_groups ADD COLUMN account_set_at TIMESTAMPTZ;

-- Supports both "groups of this client" and "groups with no client yet" (account_id IS NULL) without a scan.
CREATE INDEX idx_wa_groups_tenant_account ON wa_groups (tenant_id, account_id);
