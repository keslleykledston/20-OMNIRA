-- ADR-0018 Wave 8: the company context of a SUBJECT. It belongs to the topic, never to the conversation (one person talks
-- about several companies in one chat). At most one primary account per topic; any number of related ones. A contact that
-- belongs to several companies is never resolved by guessing: the choice is made by a person and recorded here.
CREATE TABLE topic_account_links (
  id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id          UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  topic_thread_id    UUID NOT NULL,
  account_id         UUID NOT NULL,
  relation           TEXT NOT NULL CHECK (relation IN ('primary','related')),
  -- who decided: a person (manual) or a person who CONFIRMED an AI suggestion; an AI never decides alone
  source             TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','ai_suggestion_confirmed','rule','ticket_flow','trusted_crm')),
  created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, topic_thread_id, account_id),
  FOREIGN KEY (tenant_id, topic_thread_id) REFERENCES topic_threads(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, account_id) REFERENCES customer_accounts(tenant_id, id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX topic_account_links_primary_uq ON topic_account_links (tenant_id, topic_thread_id) WHERE relation = 'primary';
CREATE INDEX topic_account_links_account_idx ON topic_account_links (tenant_id, account_id);

ALTER TABLE topic_account_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE topic_account_links FORCE ROW LEVEL SECURITY;
CREATE POLICY topic_account_links_read_tenant ON topic_account_links FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY topic_account_links_insert_tenant ON topic_account_links FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY topic_account_links_update_tenant ON topic_account_links FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY topic_account_links_delete_tenant ON topic_account_links FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON topic_account_links TO omnira_app;
