-- Subject tickets cannot coexist with the old one-per-conversation index: keep the newest active ticket of each conversation
-- as it is and close the other subject tickets (history stays) before restoring the old guarantee.
UPDATE tickets t SET status = 'closed', closed_at = now(), updated_at = now()
WHERE t.topic_scoped AND t.status IN ('open','in_progress','waiting')
  AND EXISTS (SELECT 1 FROM tickets o WHERE o.tenant_id = t.tenant_id AND o.conversation_id = t.conversation_id
              AND o.status IN ('open','in_progress','waiting') AND o.id <> t.id AND (NOT o.topic_scoped OR o.created_at > t.created_at OR (o.created_at = t.created_at AND o.id > t.id)));
DROP INDEX IF EXISTS tickets_conversation_active_idx;
DROP INDEX tickets_active_conversation_uq;
CREATE UNIQUE INDEX tickets_active_conversation_uq ON tickets (tenant_id, conversation_id) WHERE status IN ('open','in_progress','waiting');
ALTER TABLE tickets DROP COLUMN topic_scoped;
