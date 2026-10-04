-- ADR-0017: merge and split of topics are HUMAN actions that never destroy history. A merged topic is archived and keeps
-- pointing at the topic it was merged into (its messages, links and summaries stay readable); a split topic remembers
-- where it came from. There is no automatic merge by similarity (rejected in ADR-0017).
ALTER TABLE topic_threads
  ADD COLUMN merged_into_topic_id UUID,
  ADD COLUMN merged_at            TIMESTAMPTZ,
  ADD COLUMN merged_by_user_id    UUID REFERENCES users(id) ON DELETE SET NULL,
  ADD COLUMN split_from_topic_id  UUID,
  ADD CONSTRAINT topic_threads_merged_fk FOREIGN KEY (tenant_id, merged_into_topic_id) REFERENCES topic_threads(tenant_id, id) ON DELETE SET NULL (merged_into_topic_id),
  ADD CONSTRAINT topic_threads_split_fk  FOREIGN KEY (tenant_id, split_from_topic_id)  REFERENCES topic_threads(tenant_id, id) ON DELETE SET NULL (split_from_topic_id),
  ADD CONSTRAINT topic_threads_merge_coherent CHECK (merged_into_topic_id IS NULL OR (status = 'archived' AND merged_at IS NOT NULL AND merged_into_topic_id <> id)),
  ADD CONSTRAINT topic_threads_split_not_self CHECK (split_from_topic_id IS NULL OR split_from_topic_id <> id);
CREATE INDEX topic_threads_merged_into_idx ON topic_threads (tenant_id, merged_into_topic_id) WHERE merged_into_topic_id IS NOT NULL;
