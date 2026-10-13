-- ADR-0044 phase 5c: SLA. Policies by tenant, sector or queue (most specific wins, metric by metric) and the running clocks of each
-- conversation. A clock is NEVER restarted to hide a delay: a transfer keeps the time already lived and re-plans only what is left, on the
-- calendar of the place the conversation is in now. The one deliberate exception is queue_wait, which by definition measures the wait in
-- the CURRENT queue. Inert until the routing engine is on.
SET LOCAL lock_timeout = '5s';

CREATE TABLE sla_policies (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  scope_kind TEXT NOT NULL CHECK (scope_kind IN ('tenant', 'sector', 'queue')),
  sector_id UUID,
  queue_id UUID,
  first_response_seconds INTEGER CHECK (first_response_seconds BETWEEN 1 AND 2592000),
  queue_wait_seconds     INTEGER CHECK (queue_wait_seconds     BETWEEN 1 AND 2592000),
  assignment_seconds     INTEGER CHECK (assignment_seconds     BETWEEN 1 AND 2592000),
  resolution_seconds     INTEGER CHECK (resolution_seconds     BETWEEN 1 AND 7776000),
  risk_percent INTEGER NOT NULL DEFAULT 80 CHECK (risk_percent BETWEEN 1 AND 99),
  counting TEXT NOT NULL DEFAULT 'wall' CHECK (counting IN ('wall', 'business')),
  active BOOLEAN NOT NULL DEFAULT true,
  revision INTEGER NOT NULL DEFAULT 1,
  updated_by UUID REFERENCES users(id) ON DELETE SET NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((scope_kind = 'tenant' AND sector_id IS NULL AND queue_id IS NULL)
      OR (scope_kind = 'sector' AND sector_id IS NOT NULL AND queue_id IS NULL)
      OR (scope_kind = 'queue'  AND queue_id  IS NOT NULL AND sector_id IS NULL)),
  CHECK (first_response_seconds IS NOT NULL OR queue_wait_seconds IS NOT NULL OR assignment_seconds IS NOT NULL OR resolution_seconds IS NOT NULL),
  UNIQUE NULLS NOT DISTINCT (tenant_id, scope_kind, sector_id, queue_id),
  FOREIGN KEY (tenant_id, sector_id) REFERENCES sectors (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, queue_id) REFERENCES queues (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE conversation_sla (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id UUID NOT NULL,
  metric TEXT NOT NULL CHECK (metric IN ('first_response', 'queue_wait', 'assignment', 'resolution')),
  scope_episode INTEGER NOT NULL DEFAULT 0,        -- 0 for metrics that live as long as the conversation (first_response, resolution)
  queue_key UUID NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000', -- the queue, for queue_wait only
  started_at TIMESTAMPTZ NOT NULL,
  budget_seconds INTEGER NOT NULL CHECK (budget_seconds > 0),
  risk_percent INTEGER NOT NULL,
  counting TEXT NOT NULL CHECK (counting IN ('wall', 'business')),
  calendar_queue_id UUID,                          -- whose calendar counts business time right now (moves with the conversation)
  anchor_at TIMESTAMPTZ NOT NULL,                  -- the instant the remaining budget is counted from on that calendar (started_at, or the moment of the last move)
  consumed_seconds INTEGER NOT NULL DEFAULT 0 CHECK (consumed_seconds >= 0), -- time already lived before the anchor (kept across moves)
  deadline_at TIMESTAMPTZ NOT NULL,
  risk_at TIMESTAMPTZ NOT NULL,
  state TEXT NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'met', 'breached', 'cancelled')),
  risk_notified_at TIMESTAMPTZ,
  breached_at TIMESTAMPTZ,
  met_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, conversation_id, metric, scope_episode, queue_key),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_conversation_sla_open ON conversation_sla (tenant_id, state, deadline_at) WHERE state IN ('running', 'breached');

DO $$
DECLARE t TEXT; w TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['sla_policies', 'conversation_sla'] LOOP
    w := CASE WHEN t = 'conversation_sla' THEN 'is_system_admin()'
              ELSE '(is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), ''agent.manage''))' END;
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_read_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (%s)', t || '_insert_policy', t, w);
    EXECUTE format('CREATE POLICY %I ON %I FOR UPDATE USING (%s) WITH CHECK (%s)', t || '_update_policy', t, w, w);
    EXECUTE format('CREATE POLICY %I ON %I FOR DELETE USING (%s)', t || '_delete_policy', t, w);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO omnira_app', t);
  END LOOP;
END $$;
