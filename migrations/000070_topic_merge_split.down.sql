DROP INDEX IF EXISTS topic_threads_merged_into_idx;
ALTER TABLE topic_threads
  DROP CONSTRAINT IF EXISTS topic_threads_split_not_self,
  DROP CONSTRAINT IF EXISTS topic_threads_merge_coherent,
  DROP CONSTRAINT IF EXISTS topic_threads_split_fk,
  DROP CONSTRAINT IF EXISTS topic_threads_merged_fk,
  DROP COLUMN IF EXISTS split_from_topic_id,
  DROP COLUMN IF EXISTS merged_by_user_id,
  DROP COLUMN IF EXISTS merged_at,
  DROP COLUMN IF EXISTS merged_into_topic_id;
