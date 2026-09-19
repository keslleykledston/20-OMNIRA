-- W2/M05.5: outbound text sending. Idempotency is scoped to (tenant, sender, key)
-- and bound to the request content via request_hash.
ALTER TABLE messages ADD COLUMN idempotency_key TEXT;
ALTER TABLE messages ADD COLUMN request_hash TEXT;
ALTER TABLE messages ADD COLUMN sent_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE messages ADD COLUMN failure_reason TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX messages_outbound_idempotency_uq
  ON messages(tenant_id, sent_by_user_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;
