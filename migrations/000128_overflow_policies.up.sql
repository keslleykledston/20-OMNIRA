-- ADR-0044 phase 5a: overflow chains. A policy belongs to a queue (the ORIGIN); its steps are tried in order when a trigger holds.
-- Additive and inert until the routing engine is on (OMNIRA_ROUTING_ENGINE_ENABLED); a queue without a policy behaves as before.
SET LOCAL lock_timeout = '5s';

CREATE TABLE overflow_policies (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  queue_id UUID NOT NULL,
  active BOOLEAN NOT NULL DEFAULT true,
  max_steps INTEGER NOT NULL DEFAULT 4 CHECK (max_steps BETWEEN 1 AND 8),     -- hard limit of hops per episode
  on_exhausted TEXT NOT NULL DEFAULT 'keep_waiting' CHECK (on_exhausted IN ('keep_waiting', 'contingency', 'manual')),
  revision INTEGER NOT NULL DEFAULT 1,
  updated_by UUID REFERENCES users(id) ON DELETE SET NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, queue_id),
  FOREIGN KEY (tenant_id, queue_id) REFERENCES queues (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE overflow_steps (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  policy_id UUID NOT NULL,
  position INTEGER NOT NULL CHECK (position BETWEEN 1 AND 8),
  name TEXT NOT NULL DEFAULT '' CHECK (char_length(name) <= 60),
  triggers TEXT[] NOT NULL CHECK (cardinality(triggers) BETWEEN 1 AND 6
    AND triggers <@ ARRAY['after_hours', 'no_eligible_agent', 'capacity_exhausted', 'max_wait', 'sla_risk', 'sla_breach', 'operational_failure']::text[]),
  wait_seconds INTEGER NOT NULL DEFAULT 0 CHECK (wait_seconds BETWEEN 0 AND 604800),   -- 0..7 days in the step before this one
  wait_business BOOLEAN NOT NULL DEFAULT false,                                      -- count the wait in the origin's business time
  destination_kind TEXT NOT NULL CHECK (destination_kind IN ('queue', 'oncall')),
  destination_id UUID NOT NULL,
  allow_closed BOOLEAN NOT NULL DEFAULT false,                                        -- may route while the destination's calendar is closed (contingency)
  allow_off_shift BOOLEAN NOT NULL DEFAULT false,                                     -- may pick an agent outside their shift (contingency)
  UNIQUE (policy_id, position),
  FOREIGN KEY (tenant_id, policy_id) REFERENCES overflow_policies (tenant_id, id) ON DELETE CASCADE
);

DO $$
DECLARE t TEXT; w TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['overflow_policies', 'overflow_steps'] LOOP
    w := '(is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), ''agent.manage''))';
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_read_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (%s)', t || '_insert_manage', t, w);
    EXECUTE format('CREATE POLICY %I ON %I FOR UPDATE USING (%s) WITH CHECK (%s)', t || '_update_manage', t, w, w);
    EXECUTE format('CREATE POLICY %I ON %I FOR DELETE USING (%s)', t || '_delete_manage', t, w);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO omnira_app', t);
  END LOOP;
END $$;
