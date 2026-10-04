ALTER TABLE wa_groups DROP COLUMN IF EXISTS archive_purge_requested_at;
DROP INDEX IF EXISTS idx_wa_group_messages_archive;
ALTER TABLE wa_group_messages DROP CONSTRAINT IF EXISTS wa_group_messages_archive_batch_fk;
ALTER TABLE wa_group_messages DROP COLUMN IF EXISTS archive_batch_id;
DROP TABLE IF EXISTS wa_group_archive_batches;
