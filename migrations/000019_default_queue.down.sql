DROP INDEX IF EXISTS queues_one_default_per_tenant;
ALTER TABLE queues DROP COLUMN IF EXISTS is_default;
