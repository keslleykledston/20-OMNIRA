-- Removes only what is still untouched: a standard-named, non-default, manual queue with no members, no conversations and no
-- channel line pointing at it. Anything a company started using stays.
DELETE FROM queues q
WHERE q.name IN ('Comercial', 'Financeiro', 'Suporte') AND NOT q.is_default AND q.mode = 'manual'
  AND NOT EXISTS (SELECT 1 FROM queue_members m WHERE m.tenant_id = q.tenant_id AND m.queue_id = q.id)
  AND NOT EXISTS (SELECT 1 FROM conversations c WHERE c.tenant_id = q.tenant_id AND c.queue_id = q.id)
  AND NOT EXISTS (SELECT 1 FROM channel_connections cc WHERE cc.tenant_id = q.tenant_id AND cc.default_queue_id = q.id);
