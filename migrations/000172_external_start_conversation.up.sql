-- ADR-0050: create-or-reuse an outbound conversation by phone on the external API (the Gateway alert use case). Two new scopes
-- (contact.write, conversation.open) and a new idempotency-receipt operation (start_conversation). No new tables: a conversation
-- created by this command is a plain row of `conversations`, held by the calling application from the moment it is inserted.
SET LOCAL lock_timeout = '5s';

ALTER TABLE external_command_receipts DROP CONSTRAINT external_command_receipts_operation_check;
ALTER TABLE external_command_receipts ADD CONSTRAINT external_command_receipts_operation_check
  CHECK (operation IN ('claim', 'release', 'transfer', 'message', 'finalize', 'heartbeat', 'note', 'start_conversation'));

ALTER TABLE external_app_installations DROP CONSTRAINT external_app_installations_scopes_check;
ALTER TABLE external_app_installations ADD CONSTRAINT external_app_installations_scopes_check
  CHECK (cardinality(scopes) BETWEEN 1 AND 19
     AND scopes <@ ARRAY['conversation.read', 'message.read', 'media.read', 'contact.read', 'events.read',
                         'conversation.claim', 'conversation.transfer', 'conversation.finalize', 'message.send', 'service.read', 'template.read', 'message.note',
                         'group.read.external', 'contact.write', 'conversation.open']::TEXT[]);
