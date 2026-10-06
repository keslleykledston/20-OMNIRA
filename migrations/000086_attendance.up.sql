-- ADR-0020: finalizing an attendance and remembering what happened across attendances. Additive only: two new tables.
-- Tenant isolation follows the project pattern (has_active_membership/is_system_admin, FORCE RLS, grants to omnira_app).

-- One row per finalized conversation. Immutable: the app role has no UPDATE/DELETE.
CREATE TABLE conversation_closures (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id UUID NOT NULL,
  contact_id UUID NOT NULL,
  closed_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  source TEXT NOT NULL CHECK (source IN ('agent','supervisor','system')),
  reason TEXT NOT NULL CHECK (reason IN ('resolved','no_response','duplicate','spam','transferred','other')),
  note TEXT NOT NULL DEFAULT '' CHECK (char_length(note) <= 1000),
  summary TEXT NOT NULL DEFAULT '' CHECK (char_length(summary) <= 4000),
  -- ADR-0017 truth scale: what a person confirmed outranks what a model inferred.
  summary_truth TEXT NOT NULL DEFAULT 'agent_confirmed' CHECK (summary_truth IN ('agent_confirmed','ai_inferred')),
  local_tickets_closed INTEGER NOT NULL DEFAULT 0 CHECK (local_tickets_closed >= 0),
  -- active tickets left open on purpose: linked to the ERP (authoritative, ADR-0013) or scoped to a topic (ADR-0017)
  tickets_kept INTEGER NOT NULL DEFAULT 0 CHECK (tickets_kept >= 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, conversation_id),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, contact_id) REFERENCES contacts(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX conversation_closures_contact_idx ON conversation_closures(tenant_id, contact_id, created_at DESC);

-- Things to remember about a CONTACT beyond the conversation they came from: to do, promised, or worth knowing.
CREATE TABLE follow_up_items (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  contact_id UUID NOT NULL,
  conversation_id UUID NOT NULL,
  closure_id UUID,
  kind TEXT NOT NULL CHECK (kind IN ('pending','promise','info')),
  text TEXT NOT NULL CHECK (char_length(btrim(text)) BETWEEN 1 AND 500),
  owner_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  due_at TIMESTAMPTZ,
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','done','dropped')),
  truth TEXT NOT NULL DEFAULT 'agent_confirmed' CHECK (truth IN ('agent_confirmed','ai_inferred')),
  created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at TIMESTAMPTZ,
  resolved_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  resolution_note TEXT NOT NULL DEFAULT '' CHECK (char_length(resolution_note) <= 500),
  CHECK ((status = 'open') = (resolved_at IS NULL)),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, contact_id) REFERENCES contacts(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, closure_id) REFERENCES conversation_closures(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX follow_up_items_contact_idx ON follow_up_items(tenant_id, contact_id, status, created_at DESC);
CREATE INDEX follow_up_items_owner_open_idx ON follow_up_items(tenant_id, owner_user_id) WHERE status = 'open';

DO $att$
DECLARE
  t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['conversation_closures','follow_up_items'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I_read_tenant ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t, t);
    EXECUTE format('CREATE POLICY %I_insert_tenant ON %I FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t, t);
    EXECUTE format('CREATE POLICY %I_update_tenant ON %I FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t, t);
    EXECUTE format('CREATE POLICY %I_delete_tenant ON %I FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t, t);
  END LOOP;
END $att$;

GRANT SELECT, INSERT ON conversation_closures TO omnira_app;   -- immutable: no UPDATE/DELETE
GRANT SELECT, INSERT, UPDATE ON follow_up_items TO omnira_app;
