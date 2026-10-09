-- ADR-0038 phase 4: who answers for each instance. A hub keeps WORK POOLS (groups of its agents); a pool answers for instances (optionally
-- one queue of an instance) and can distribute new conversations by itself. Nothing here GRANTS access: a pool only chooses among the
-- people who already hold a live, reply-capable grant on the instance (has_active_hub_access is asked again, inside the assignment's
-- own transaction, for the person chosen).
ALTER TABLE work_pools
  ADD COLUMN distribution TEXT NOT NULL DEFAULT 'manual' CHECK (distribution IN ('manual', 'round_robin'));

-- A pool member must be a member of the pool's own hub, and leaving the hub removes them from every pool.
ALTER TABLE work_pool_members
  ADD COLUMN hub_id UUID,
  ADD COLUMN max_open INTEGER NOT NULL DEFAULT 10 CHECK (max_open BETWEEN 1 AND 500),
  ADD COLUMN last_assigned_at TIMESTAMPTZ;
UPDATE work_pool_members m SET hub_id = p.hub_id FROM work_pools p WHERE p.id = m.work_pool_id;
ALTER TABLE work_pool_members ALTER COLUMN hub_id SET NOT NULL;
ALTER TABLE work_pool_members
  ADD CONSTRAINT work_pool_members_pool_hub_fk FOREIGN KEY (work_pool_id, hub_id) REFERENCES work_pools (id, hub_id) ON DELETE CASCADE,
  ADD CONSTRAINT work_pool_members_hub_member_fk FOREIGN KEY (hub_id, user_id) REFERENCES hub_memberships (hub_id, user_id) ON DELETE CASCADE;

-- Which instances (and, optionally, which queue of one) a pool answers for. At most ONE pool answers for a given (instance, queue):
-- NULL queue = the whole instance, and NULLS NOT DISTINCT makes that unique too.
CREATE TABLE work_pool_instances (
  id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  work_pool_id UUID NOT NULL,
  hub_id       UUID NOT NULL,
  tenant_id    UUID NOT NULL,
  queue_id     UUID,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  FOREIGN KEY (work_pool_id, hub_id) REFERENCES work_pools (id, hub_id) ON DELETE CASCADE,
  FOREIGN KEY (hub_id, tenant_id) REFERENCES hub_tenant_service_contracts (hub_id, tenant_id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, queue_id) REFERENCES queues (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT work_pool_instances_one_pool_per_scope UNIQUE NULLS NOT DISTINCT (hub_id, tenant_id, queue_id)
);
CREATE INDEX work_pool_instances_pool_idx ON work_pool_instances(work_pool_id);
CREATE INDEX work_pool_instances_tenant_idx ON work_pool_instances(tenant_id);

ALTER TABLE work_pool_instances ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_pool_instances FORCE ROW LEVEL SECURITY;
CREATE POLICY work_pool_instances_read ON work_pool_instances FOR SELECT USING (is_hub_member(hub_id, current_user_id()) OR is_system_admin());
CREATE POLICY work_pool_instances_insert ON work_pool_instances FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY work_pool_instances_update ON work_pool_instances FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY work_pool_instances_delete ON work_pool_instances FOR DELETE USING (is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON work_pool_instances TO omnira_app;
