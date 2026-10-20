SET LOCAL lock_timeout = '5s';

UPDATE external_app_installations SET scopes = array_remove(array_remove(scopes, 'contact.write'), 'conversation.open')
 WHERE ('contact.write' = ANY (scopes) OR 'conversation.open' = ANY (scopes))
   AND cardinality(array_remove(array_remove(scopes, 'contact.write'), 'conversation.open')) > 0;
UPDATE external_app_installations SET scopes = ARRAY['events.read'], status = CASE WHEN status = 'revoked' THEN status ELSE 'suspended' END,
       suspended_at = CASE WHEN status = 'revoked' THEN suspended_at ELSE COALESCE(suspended_at, now()) END
 WHERE 'contact.write' = ANY (scopes) OR 'conversation.open' = ANY (scopes);

ALTER TABLE external_app_installations DROP CONSTRAINT external_app_installations_scopes_check;
ALTER TABLE external_app_installations ADD CONSTRAINT external_app_installations_scopes_check
  CHECK (cardinality(scopes) BETWEEN 1 AND 19
     AND scopes <@ ARRAY['conversation.read', 'message.read', 'media.read', 'contact.read', 'events.read',
                         'conversation.claim', 'conversation.transfer', 'conversation.finalize', 'message.send', 'service.read', 'template.read', 'message.note',
                         'group.read.external']::TEXT[]);

-- Unlike a private note (ADR-0053, throwaway internal-only data), a conversation this command created is a REAL conversation that may already
-- carry a real history with the customer — rolling back this migration must never delete it. Only the command's own dedup bookkeeping goes.
DELETE FROM external_command_receipts WHERE operation = 'start_conversation';
ALTER TABLE external_command_receipts DROP CONSTRAINT external_command_receipts_operation_check;
ALTER TABLE external_command_receipts ADD CONSTRAINT external_command_receipts_operation_check
  CHECK (operation IN ('claim', 'release', 'transfer', 'message', 'finalize', 'heartbeat', 'note'));
