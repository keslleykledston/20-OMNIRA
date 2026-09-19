-- M04: tenant-owned queues, membership and append-only assignment history.
CREATE TABLE queues (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  mode TEXT NOT NULL DEFAULT 'manual' CHECK (mode IN ('manual', 'round_robin')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, name)
);

CREATE TABLE queue_members (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  queue_id UUID NOT NULL,
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  active BOOLEAN NOT NULL DEFAULT true,
  available BOOLEAN NOT NULL DEFAULT true,
  capacity INTEGER NOT NULL DEFAULT 1 CHECK (capacity > 0 AND capacity <= 100),
  last_assigned_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, queue_id, user_id),
  FOREIGN KEY (tenant_id, queue_id) REFERENCES queues(tenant_id, id) ON DELETE CASCADE
);

ALTER TABLE conversations ADD COLUMN queue_id UUID;
ALTER TABLE conversations ADD COLUMN assigned_to_user_id UUID REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE conversations ADD COLUMN assigned_at TIMESTAMPTZ;
ALTER TABLE conversations ADD CONSTRAINT conversations_queue_tenant_fk
  FOREIGN KEY (tenant_id, queue_id) REFERENCES queues(tenant_id, id) ON DELETE RESTRICT;

CREATE INDEX idx_queue_members_eligible ON queue_members(tenant_id, queue_id, active, available, last_assigned_at);
CREATE INDEX idx_conversations_queue_assignment ON conversations(tenant_id, queue_id, assigned_to_user_id, updated_at);

CREATE TABLE assignment_events (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id UUID NOT NULL,
  from_user_id UUID,
  to_user_id UUID,
  changed_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  reason TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_assignment_events_tenant_conversation ON assignment_events(tenant_id, conversation_id, created_at, id);

DO $m04$
DECLARE
  table_name TEXT;
BEGIN
  FOREACH table_name IN ARRAY ARRAY['queues', 'queue_members', 'assignment_events'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format('CREATE POLICY %I_read_tenant ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', table_name, table_name);
    EXECUTE format('CREATE POLICY %I_insert_tenant ON %I FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', table_name, table_name);
    EXECUTE format('CREATE POLICY %I_update_tenant ON %I FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', table_name, table_name);
    EXECUTE format('CREATE POLICY %I_delete_tenant ON %I FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', table_name, table_name);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO omnira_app', table_name);
  END LOOP;
END $m04$;

REVOKE UPDATE, DELETE ON assignment_events FROM omnira_app;
GRANT UPDATE ON conversations TO omnira_app;
