-- ADR-0053: internal note on the external API, as a message that is NEVER queued for delivery (paridade Chatwoot's `private: true`).
-- `private` is a plain column, not a status value, so "is this a note" and "what happened to delivery" never mix in the same field
-- (see ADR-0053 Alternativas). The actual barrier against a note reaching WAHA/Meta lives in the application code that inserts it
-- (internal/messages/adapters/outbound_note.go): that statement has no job/outbox CTE, ever. This migration only shapes the data.
SET LOCAL lock_timeout = '5s';

ALTER TABLE messages ADD COLUMN private BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE messages DROP CONSTRAINT messages_status_check;
ALTER TABLE messages ADD CONSTRAINT messages_status_check
  CHECK (status IN ('received', 'queued', 'sent', 'delivered', 'read', 'failed', 'uncertain', 'noted'));

-- 'noted' exists only for a private note, and a private note always carries it: the two concepts stay tied so a future bug cannot create a
-- "private" row with a real delivery status, or a "noted" row that is not private.
ALTER TABLE messages ADD CONSTRAINT messages_private_status_consistent
  CHECK ((private AND status = 'noted') OR (NOT private AND status <> 'noted'));

-- the sixth command: same idempotency-receipt table as claim/release/transfer/message/finalize/heartbeat (write_http.go's generic cmdSpec/handle).
ALTER TABLE external_command_receipts DROP CONSTRAINT external_command_receipts_operation_check;
ALTER TABLE external_command_receipts ADD CONSTRAINT external_command_receipts_operation_check
  CHECK (operation IN ('claim', 'release', 'transfer', 'message', 'finalize', 'heartbeat', 'note'));

-- the new scope: an installation may record notes only if the administrator granted it, independent of message.send.
ALTER TABLE external_app_installations DROP CONSTRAINT external_app_installations_scopes_check;
ALTER TABLE external_app_installations ADD CONSTRAINT external_app_installations_scopes_check
  CHECK (cardinality(scopes) BETWEEN 1 AND 19
     AND scopes <@ ARRAY['conversation.read', 'message.read', 'media.read', 'contact.read', 'events.read',
                         'conversation.claim', 'conversation.transfer', 'conversation.finalize', 'message.send', 'service.read', 'template.read', 'message.note']::TEXT[]);
