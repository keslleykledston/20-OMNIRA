DROP TABLE IF EXISTS assignment_events CASCADE;
ALTER TABLE conversations DROP CONSTRAINT IF EXISTS conversations_queue_tenant_fk;
ALTER TABLE conversations DROP COLUMN IF EXISTS assigned_at;
ALTER TABLE conversations DROP COLUMN IF EXISTS assigned_to_user_id;
ALTER TABLE conversations DROP COLUMN IF EXISTS queue_id;
DROP TABLE IF EXISTS queue_members CASCADE;
DROP TABLE IF EXISTS queues CASCADE;
