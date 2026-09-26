-- Safe only if no row is currently 'uncertain' — same category as any other
-- enum-narrowing rollback. Fails honestly (constraint violation) otherwise.
ALTER TABLE messages DROP CONSTRAINT messages_status_check;
ALTER TABLE messages ADD CONSTRAINT messages_status_check
  CHECK (status IN ('received', 'queued', 'sent', 'delivered', 'read', 'failed'));
