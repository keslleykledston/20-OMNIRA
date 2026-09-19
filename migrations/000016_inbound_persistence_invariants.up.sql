-- M03.2: provider message identity and concurrent inbound invariants.
ALTER TABLE messages ADD COLUMN channel_connection_id UUID;

UPDATE messages m
SET channel_connection_id = c.channel_connection_id
FROM conversations c
WHERE c.tenant_id = m.tenant_id
  AND c.id = m.conversation_id
  AND c.channel_connection_id IS NOT NULL;

ALTER TABLE messages ADD CONSTRAINT messages_channel_connection_tenant_fk
  FOREIGN KEY (tenant_id, channel_connection_id)
  REFERENCES channel_connections(tenant_id, id) ON DELETE RESTRICT;

DROP INDEX messages_provider_id_uq;
CREATE UNIQUE INDEX messages_provider_connection_id_uq
  ON messages(tenant_id, channel_connection_id, provider_message_id)
  WHERE provider_message_id <> '' AND channel_connection_id IS NOT NULL;

CREATE UNIQUE INDEX conversations_open_contact_channel_uq
  ON conversations(tenant_id, contact_id, channel_connection_id)
  WHERE status = 'open' AND channel_connection_id IS NOT NULL;

CREATE UNIQUE INDEX tickets_active_conversation_uq
  ON tickets(tenant_id, conversation_id)
  WHERE status IN ('open', 'in_progress', 'waiting');
