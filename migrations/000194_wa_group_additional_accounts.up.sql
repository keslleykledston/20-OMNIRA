-- ADR-0059 addendum 8: some WhatsApp groups genuinely concern MORE THAN ONE of the tenant's CRM clients at
-- once — e.g. a "swap"/peering group between two ISPs the tenant operates on behalf of both of, where a
-- ticket (when one is needed) may be registered against either client or both, decided by whoever is asked.
-- wa_groups.account_id (migration 000179) stays exactly as is: the group's ONE primary/default client, used
-- everywhere it already is today (GroupList/GroupThread badge, the Hub Organizer's "Cliente do CRM" folders,
-- hub_group_catalog_relationship_context). This migration adds a SEPARATE, purely additive N:N table for
-- every OTHER client a group also concerns — never a replacement, never touched by LinkAccount/UnlinkAccount.
CREATE TABLE wa_group_additional_accounts (
  tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  group_id    UUID NOT NULL,
  account_id  UUID NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
  PRIMARY KEY (group_id, account_id),
  CONSTRAINT wa_group_additional_accounts_group_fk FOREIGN KEY (tenant_id, group_id) REFERENCES wa_groups (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT wa_group_additional_accounts_account_fk FOREIGN KEY (tenant_id, account_id) REFERENCES customer_accounts (tenant_id, id) ON DELETE RESTRICT
);
CREATE INDEX idx_wa_group_additional_accounts_tenant_account ON wa_group_additional_accounts (tenant_id, account_id);

ALTER TABLE wa_group_additional_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE wa_group_additional_accounts FORCE ROW LEVEL SECURITY;
-- Same tenant-membership predicate as wa_groups itself (migration 000057) — this table carries no
-- privilege of its own, it is just a side table of wa_groups.
CREATE POLICY wa_group_additional_accounts_read_tenant ON wa_group_additional_accounts
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_additional_accounts_insert_tenant ON wa_group_additional_accounts
  FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_additional_accounts_delete_tenant ON wa_group_additional_accounts
  FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());

GRANT SELECT, INSERT, DELETE ON wa_group_additional_accounts TO omnira_app;
