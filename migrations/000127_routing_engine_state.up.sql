-- ADR-0044 phase 4: the state, the decision history and the durable timers of the central routing engine. Additive; the engine is OFF
-- by default (OMNIRA_ROUTING_ENGINE_ENABLED), so nothing here changes routing until the flag is turned on.
SET LOCAL lock_timeout = '5s';

-- third distribution strategy: the member with the fewest open conversations in the queue
ALTER TABLE queues DROP CONSTRAINT IF EXISTS queues_mode_check;
ALTER TABLE queues ADD CONSTRAINT queues_mode_check CHECK (mode IN ('manual', 'round_robin', 'least_loaded'));

CREATE TABLE routing_engine_state (
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id UUID NOT NULL,
  episode INTEGER NOT NULL DEFAULT 1 CHECK (episode >= 1),   -- a new episode starts only with a HUMAN action; automatic requeue never resets the loop guard
  step_index INTEGER NOT NULL DEFAULT 0 CHECK (step_index >= 0),
  state TEXT NOT NULL CHECK (state IN ('queued', 'waiting_open', 'assigned', 'manual_required', 'flow', 'done')),
  queue_id UUID,
  visited TEXT[] NOT NULL DEFAULT '{}' CHECK (cardinality(visited) <= 32), -- canonical destination keys already tried in this episode
  hops INTEGER NOT NULL DEFAULT 0 CHECK (hops >= 0),
  entered_queue_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  generation INTEGER NOT NULL DEFAULT 1,
  last_decision TEXT,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, conversation_id),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, queue_id) REFERENCES queues (tenant_id, id) ON DELETE SET NULL (queue_id)
);
CREATE INDEX idx_routing_engine_state_waiting ON routing_engine_state (tenant_id, state) WHERE state IN ('waiting_open', 'manual_required');

-- one row per (conversation, episode, step, trigger): repeating a trigger (duplicate event, retry, two workers) updates THIS row
-- (attempts + 1), it never creates a second decision
CREATE TABLE routing_engine_decisions (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id UUID NOT NULL,
  episode INTEGER NOT NULL,
  step_index INTEGER NOT NULL DEFAULT 0,
  trigger TEXT NOT NULL CHECK (char_length(trigger) BETWEEN 1 AND 40),
  decision TEXT NOT NULL CHECK (decision IN ('ASSIGN_AGENT', 'ENQUEUE', 'OVERFLOW', 'WAIT_UNTIL_OPEN', 'EXECUTE_FLOW', 'ESCALATE', 'REQUIRE_MANUAL_ACTION', 'NO_ACTION')),
  destination TEXT NOT NULL DEFAULT '' CHECK (char_length(destination) <= 80),
  reason TEXT NOT NULL DEFAULT '' CHECK (char_length(reason) <= 60),
  detail JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (octet_length(detail::text) <= 4096), -- ids and counters only, never message content
  attempts INTEGER NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, conversation_id, episode, step_index, trigger),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_routing_engine_decisions_conversation ON routing_engine_decisions (tenant_id, conversation_id, updated_at DESC);

-- durable timers: the due time lives in the database, never only in a process
CREATE TABLE routing_engine_timers (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id UUID NOT NULL,
  episode INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('open_wakeup', 'step_wait', 'sla_risk', 'sla_breach', 'retry')),
  step_index INTEGER NOT NULL DEFAULT 0,
  due_at TIMESTAMPTZ NOT NULL,
  generation INTEGER NOT NULL DEFAULT 1,             -- bumped when the timer is re-planned; a worker holding an old generation cannot complete it
  state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'leased', 'done', 'cancelled', 'failed')),
  lease_until TIMESTAMPTZ,
  claim_token UUID,                                  -- fencing: set at every claim; only the holder of the CURRENT token may finish, fail or release the timer
  attempts INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '' CHECK (char_length(last_error) <= 200),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, conversation_id, episode, kind, step_index),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_routing_engine_timers_due ON routing_engine_timers (due_at) WHERE state IN ('pending', 'leased');

-- RLS: members READ (the screens, agent.read is checked by the handlers); WRITES are the engine's (system session) or, for state and timers,
-- a person who may manage agents (a calendar change re-plans the timers of the waiting conversations in the request that made the change).
-- Decisions are written by the engine only.
DO $$
DECLARE t TEXT; w TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['routing_engine_state', 'routing_engine_decisions', 'routing_engine_timers'] LOOP
    w := CASE WHEN t = 'routing_engine_decisions' THEN 'is_system_admin()'
              ELSE '(is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), ''agent.manage''))' END;
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_read_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (%s)', t || '_insert_engine', t, w);
    EXECUTE format('CREATE POLICY %I ON %I FOR UPDATE USING (%s) WITH CHECK (%s)', t || '_update_engine', t, w, w);
    EXECUTE format('CREATE POLICY %I ON %I FOR DELETE USING (%s)', t || '_delete_engine', t, w);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO omnira_app', t);
  END LOOP;
END $$;
