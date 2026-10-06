-- FLOW.1 (ADR-0019): conversational flows. Additive only: new tables, no existing table is altered here.
-- Tenant isolation follows the project pattern (has_active_membership/is_system_admin, FORCE RLS, grants to omnira_app).

CREATE TABLE flows (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  slug TEXT NOT NULL CHECK (slug ~ '^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$'),
  name TEXT NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 120),
  description TEXT NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
  flow_type TEXT NOT NULL DEFAULT 'INBOUND'
    CHECK (flow_type IN ('INBOUND','INTERNAL','SUBFLOW','EVENT','SURVEY','AFTER_HOURS','AI_WORKFLOW')),
  -- draft: never published; published: has an active version; archived: out of use.
  status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published','archived')),
  draft_definition JSONB NOT NULL
    DEFAULT '{"schema_version":1,"nodes":[],"edges":[],"variables":[],"settings":{},"metadata":{}}'::jsonb
    CHECK (octet_length(draft_definition::text) <= 1048576),
  -- Optimistic concurrency for the editor: every draft save must present the revision it read.
  draft_revision INTEGER NOT NULL DEFAULT 1 CHECK (draft_revision >= 1),
  active_version_id UUID,
  priority INTEGER NOT NULL DEFAULT 100 CHECK (priority BETWEEN 1 AND 1000),
  is_default BOOLEAN NOT NULL DEFAULT false,
  -- {"connection_ids":[uuid...],"providers":["waha","meta_cloud"]}; empty = every channel line of the tenant.
  trigger_filter JSONB NOT NULL DEFAULT '{}'::jsonb,
  -- New conversations only (default) or every inbound once the previous run ended.
  restart_policy TEXT NOT NULL DEFAULT 'new_conversation_only'
    CHECK (restart_policy IN ('new_conversation_only','always')),
  -- Provenance only; never an operational dependency on the template.
  source_template_slug TEXT,
  source_template_version INTEGER,
  template_installed_at TIMESTAMPTZ,
  created_by UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  archived_at TIMESTAMPTZ,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, slug),
  CHECK ((status = 'published') = (active_version_id IS NOT NULL) OR status = 'archived')
);
-- At most one live default flow per tenant and type.
CREATE UNIQUE INDEX flows_one_default_per_type_uq ON flows(tenant_id, flow_type) WHERE is_default AND status <> 'archived';
CREATE INDEX flows_resolver_idx ON flows(tenant_id, flow_type, priority, id) WHERE status = 'published';
CREATE INDEX flows_tenant_updated_idx ON flows(tenant_id, updated_at DESC, id DESC);

CREATE TABLE flow_versions (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  flow_id UUID NOT NULL,
  version INTEGER NOT NULL CHECK (version >= 1),
  definition JSONB NOT NULL CHECK (octet_length(definition::text) <= 1048576),
  definition_hash TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '' CHECK (char_length(note) <= 500),
  -- subflow slug -> flow_versions.id, resolved ONCE at publish: a run never follows "the active version" of a
  -- subflow, so a later publish of the subflow cannot change a run (or this version) in flight.
  subflow_pins JSONB NOT NULL DEFAULT '{}'::jsonb,
  published_by UUID REFERENCES users(id) ON DELETE SET NULL,
  published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (flow_id, version),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, flow_id) REFERENCES flows(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX flow_versions_flow_idx ON flow_versions(tenant_id, flow_id, version DESC);

-- The active pointer is deferred: a tenant/flow cascade deletes both sides in one statement.
ALTER TABLE flows ADD CONSTRAINT flows_active_version_fk
  FOREIGN KEY (tenant_id, active_version_id) REFERENCES flow_versions(tenant_id, id) DEFERRABLE INITIALLY DEFERRED;

-- A published version never changes (defense in depth: omnira_app also has no UPDATE/DELETE grant).
CREATE FUNCTION flow_versions_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'flow_versions are immutable (version %)', OLD.version USING ERRCODE = 'integrity_constraint_violation';
END $$;
CREATE TRIGGER flow_versions_no_update BEFORE UPDATE ON flow_versions FOR EACH ROW EXECUTE FUNCTION flow_versions_immutable();

CREATE TABLE flow_runs (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  flow_id UUID NOT NULL,
  flow_version_id UUID NOT NULL,
  conversation_id UUID NOT NULL,
  contact_id UUID,
  active_customer_account_id UUID,
  status TEXT NOT NULL CHECK (status IN ('running','waiting_input','waiting_human','completed','failed','cancelled','expired')),
  current_node_id TEXT,
  variables JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (octet_length(variables::text) <= 262144),
  call_stack JSONB NOT NULL DEFAULT '[]'::jsonb,
  wait_until TIMESTAMPTZ,
  -- Idempotency: the inbound event (message id) that started the run.
  trigger_event_id TEXT NOT NULL CHECK (char_length(trigger_event_id) BETWEEN 1 AND 200),
  node_exec_count INTEGER NOT NULL DEFAULT 0,
  last_event_id TEXT,
  error TEXT,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, trigger_event_id),
  FOREIGN KEY (tenant_id, flow_id) REFERENCES flows(tenant_id, id),
  FOREIGN KEY (tenant_id, flow_version_id) REFERENCES flow_versions(tenant_id, id),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, contact_id) REFERENCES contacts(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, active_customer_account_id) REFERENCES customer_accounts(tenant_id, id)
);
-- One active run per conversation: two near-simultaneous inbound events can never fork the conversation.
CREATE UNIQUE INDEX flow_runs_one_active_per_conversation_uq ON flow_runs(tenant_id, conversation_id)
  WHERE status IN ('running','waiting_input','waiting_human');
CREATE INDEX flow_runs_due_idx ON flow_runs(wait_until) WHERE status = 'waiting_input' AND wait_until IS NOT NULL;
CREATE INDEX flow_runs_flow_idx ON flow_runs(tenant_id, flow_id, started_at DESC);
CREATE INDEX flow_runs_conversation_idx ON flow_runs(tenant_id, conversation_id, started_at DESC);

CREATE TABLE flow_node_executions (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  flow_run_id UUID NOT NULL,
  seq INTEGER NOT NULL CHECK (seq >= 1),
  flow_version_id UUID NOT NULL,
  node_id TEXT NOT NULL,
  node_type TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('completed','failed','waiting','skipped')),
  port TEXT,
  -- input/output are persisted already redacted (see internal/flows/domain redaction).
  input JSONB NOT NULL DEFAULT '{}'::jsonb,
  output JSONB NOT NULL DEFAULT '{}'::jsonb,
  error TEXT,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  duration_ms INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
  UNIQUE (flow_run_id, seq),
  FOREIGN KEY (tenant_id, flow_run_id) REFERENCES flow_runs(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, flow_version_id) REFERENCES flow_versions(tenant_id, id)
);
CREATE INDEX flow_node_executions_run_idx ON flow_node_executions(tenant_id, flow_run_id, seq);

CREATE TABLE flow_pack_installations (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  pack_slug TEXT NOT NULL,
  pack_version INTEGER NOT NULL CHECK (pack_version >= 1),
  selected_templates JSONB NOT NULL DEFAULT '[]'::jsonb,
  mappings JSONB NOT NULL DEFAULT '{}'::jsonb,
  installed_by UUID REFERENCES users(id) ON DELETE SET NULL,
  installed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id)
);

CREATE TABLE flow_template_installations (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  template_slug TEXT NOT NULL,
  template_version INTEGER NOT NULL CHECK (template_version >= 1),
  flow_id UUID NOT NULL,
  pack_installation_id UUID,
  -- Resource ids only (queues, teams...). Never credentials.
  mappings JSONB NOT NULL DEFAULT '{}'::jsonb,
  installed_by UUID REFERENCES users(id) ON DELETE SET NULL,
  installed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, flow_id) REFERENCES flows(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, pack_installation_id) REFERENCES flow_pack_installations(tenant_id, id) ON DELETE SET NULL (pack_installation_id)
);
CREATE INDEX flow_template_installations_flow_idx ON flow_template_installations(tenant_id, flow_id);

DO $flow$
DECLARE
  t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['flows','flow_versions','flow_runs','flow_node_executions','flow_pack_installations','flow_template_installations'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I_read_tenant ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t, t);
    EXECUTE format('CREATE POLICY %I_insert_tenant ON %I FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t, t);
    EXECUTE format('CREATE POLICY %I_update_tenant ON %I FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t, t);
    EXECUTE format('CREATE POLICY %I_delete_tenant ON %I FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t, t);
  END LOOP;
END $flow$;

GRANT SELECT, INSERT, UPDATE ON flows TO omnira_app;
GRANT SELECT, INSERT ON flow_versions TO omnira_app;                 -- immutable: no UPDATE/DELETE
GRANT SELECT, INSERT, UPDATE ON flow_runs TO omnira_app;
GRANT SELECT, INSERT ON flow_node_executions TO omnira_app;          -- append-only audit trail
GRANT SELECT, INSERT ON flow_pack_installations TO omnira_app;
GRANT SELECT, INSERT ON flow_template_installations TO omnira_app;
