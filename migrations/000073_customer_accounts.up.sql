-- ADR-0018 Wave 1: CustomerAccount, the LOCAL and stable identity of an organization the tenant serves. A provider
-- (K3G) company id is only a LINK to it (account_external_links), scoped by provider and connection: the CRM can change
-- without making a person's history with a company disappear.
CREATE TABLE customer_accounts (
  id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name          TEXT NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
  -- 'internal' is an internal ORGANIZATION account, never a person (staff are Users, ADR-0018)
  account_type  TEXT NOT NULL DEFAULT 'customer' CHECK (account_type IN ('customer','partner','internal','other')),
  status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','inactive','archived')),
  metadata      JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (pg_column_size(metadata) <= 8192),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  archived_at   TIMESTAMPTZ,
  UNIQUE (tenant_id, id),
  CHECK ((status = 'archived') = (archived_at IS NOT NULL))
);
CREATE INDEX customer_accounts_tenant_status_idx ON customer_accounts (tenant_id, status, lower(name));
CREATE INDEX customer_accounts_name_idx ON customer_accounts (tenant_id, lower(name) text_pattern_ops);

CREATE TABLE account_external_links (
  id                    UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id             UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  account_id            UUID NOT NULL,
  provider              TEXT NOT NULL CHECK (char_length(provider) BETWEEN 1 AND 40),
  connection_id         UUID NOT NULL,
  external_company_id   TEXT NOT NULL CHECK (char_length(btrim(external_company_id)) BETWEEN 1 AND 128),
  external_name_snapshot TEXT CHECK (external_name_snapshot IS NULL OR char_length(external_name_snapshot) <= 200),
  status                TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','inactive')),
  source                TEXT NOT NULL CHECK (source IN ('directory_selection','ticket_flow','crm_evidence','import')),
  verified_at           TIMESTAMPTZ,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  -- an external id is scoped by provider AND connection: the same value on another connection is another company
  UNIQUE (tenant_id, provider, connection_id, external_company_id),
  FOREIGN KEY (tenant_id, account_id) REFERENCES customer_accounts(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, connection_id) REFERENCES channel_connections(tenant_id, id) ON DELETE RESTRICT
);
CREATE INDEX account_external_links_account_idx ON account_external_links (tenant_id, account_id);

DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['customer_accounts','account_external_links'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_read_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_insert_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_update_tenant', t);
    -- no DELETE policy: an account is archived, never erased (history of people and tickets points at it)
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO omnira_app', t);
  END LOOP;
END $$;

-- Permissions (ADR-0018). Authority is always the permission matrix, never a role name.
--   account.read       read customer accounts                              admin, supervisor, agent
--   account.manage     edit / archive accounts directly                    admin, supervisor
--   contact.classify   classify contacts and link/unlink their companies   admin, supervisor, agent
--   identity.manage    manage verified internal channel identities         admin
INSERT INTO permissions(key, description) VALUES
  ('account.read',     'Read the customer accounts (organizations) the tenant serves'),
  ('account.manage',   'Edit and archive customer accounts'),
  ('contact.classify', 'Classify contacts and link or unlink their companies'),
  ('identity.manage',  'Manage the verified channel identities of internal users')
ON CONFLICT (key) DO NOTHING;
INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, p.key FROM roles r CROSS JOIN permissions p
WHERE r.tenant_id IS NULL AND (
     (r.key IN ('tenant_admin','tenant_supervisor','tenant_agent') AND p.key IN ('account.read','contact.classify'))
  OR (r.key IN ('tenant_admin','tenant_supervisor') AND p.key = 'account.manage')
  OR (r.key = 'tenant_admin' AND p.key = 'identity.manage'))
ON CONFLICT DO NOTHING;
