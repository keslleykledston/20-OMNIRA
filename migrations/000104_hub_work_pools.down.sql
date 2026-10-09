DROP TABLE IF EXISTS work_pool_instances;
ALTER TABLE work_pool_members
  DROP CONSTRAINT IF EXISTS work_pool_members_hub_member_fk,
  DROP CONSTRAINT IF EXISTS work_pool_members_pool_hub_fk,
  DROP COLUMN IF EXISTS last_assigned_at,
  DROP COLUMN IF EXISTS max_open,
  DROP COLUMN IF EXISTS hub_id;
ALTER TABLE work_pools DROP COLUMN IF EXISTS distribution;
