-- ADR-0018 Wave 7: the OMNIRA customer account a ticket targets. A PROJECTION: it is set once, server-side, from the
-- company the tenant's own CompanyDirectory validated for the external ticket (resolved through account_external_links),
-- never from a browser-supplied id. The provider company stays reachable through the account's external link.
ALTER TABLE tickets ADD COLUMN customer_account_id UUID;
ALTER TABLE tickets ADD CONSTRAINT tickets_customer_account_fk
  FOREIGN KEY (tenant_id, customer_account_id) REFERENCES customer_accounts(tenant_id, id) ON DELETE RESTRICT;
CREATE INDEX tickets_customer_account_idx ON tickets (tenant_id, customer_account_id, updated_at DESC) WHERE customer_account_id IS NOT NULL;
