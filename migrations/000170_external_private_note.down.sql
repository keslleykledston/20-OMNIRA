SET LOCAL lock_timeout = '5s';

-- an installation never disappears with this rollback: the scope is taken away; one whose ONLY scope it was keeps events.read and is suspended
-- (it can do nothing until an administrator reviews it) — same pattern as 000164/000169.
UPDATE external_app_installations SET scopes = array_remove(scopes, 'message.note') WHERE 'message.note' = ANY (scopes) AND cardinality(scopes) > 1;
UPDATE external_app_installations SET scopes = ARRAY['events.read'], status = CASE WHEN status = 'revoked' THEN status ELSE 'suspended' END,
       suspended_at = CASE WHEN status = 'revoked' THEN suspended_at ELSE COALESCE(suspended_at, now()) END
 WHERE 'message.note' = ANY (scopes);

ALTER TABLE external_app_installations DROP CONSTRAINT external_app_installations_scopes_check;
ALTER TABLE external_app_installations ADD CONSTRAINT external_app_installations_scopes_check
  CHECK (cardinality(scopes) BETWEEN 1 AND 18
     AND scopes <@ ARRAY['conversation.read', 'message.read', 'media.read', 'contact.read', 'events.read',
                         'conversation.claim', 'conversation.transfer', 'conversation.finalize', 'message.send', 'service.read', 'template.read']::TEXT[]);

-- private notes have no meaning once the feature is rolled back; their webhook deliveries (if any were ever enqueued) go with them.
DELETE FROM webhook_deliveries d USING domain_events e WHERE e.id = d.event_id AND e.aggregate_type = 'message'
  AND e.aggregate_id IN (SELECT id::text FROM messages WHERE private);
DELETE FROM messages WHERE private;

DELETE FROM external_command_receipts WHERE operation = 'note';
ALTER TABLE external_command_receipts DROP CONSTRAINT external_command_receipts_operation_check;
ALTER TABLE external_command_receipts ADD CONSTRAINT external_command_receipts_operation_check
  CHECK (operation IN ('claim', 'release', 'transfer', 'message', 'finalize', 'heartbeat'));

ALTER TABLE messages DROP CONSTRAINT messages_private_status_consistent;

ALTER TABLE messages DROP CONSTRAINT messages_status_check;
ALTER TABLE messages ADD CONSTRAINT messages_status_check
  CHECK (status IN ('received', 'queued', 'sent', 'delivered', 'read', 'failed', 'uncertain'));

ALTER TABLE messages DROP COLUMN private;
