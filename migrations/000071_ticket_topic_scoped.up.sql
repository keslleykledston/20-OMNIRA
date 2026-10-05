-- A conversation can now carry more than one active ticket when a person (or the policy, on a person's click) opens a
-- ticket for a NEW SUBJECT of the same conversation (ADR-0017). The ticket that inbound creates for the conversation (the
-- "conversation ticket") keeps its guarantee of at most one active per conversation, which is what protects against two
-- concurrent inbound messages creating two of them. Tickets opened for a subject are marked topic_scoped and are outside it.
ALTER TABLE tickets ADD COLUMN topic_scoped BOOLEAN NOT NULL DEFAULT false;
DROP INDEX tickets_active_conversation_uq;
CREATE UNIQUE INDEX tickets_active_conversation_uq ON tickets (tenant_id, conversation_id)
  WHERE status IN ('open','in_progress','waiting') AND NOT topic_scoped;
CREATE INDEX tickets_conversation_active_idx ON tickets (tenant_id, conversation_id, topic_scoped, updated_at DESC)
  WHERE status IN ('open','in_progress','waiting');
