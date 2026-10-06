-- FLOW.3 (ADR-0019): additive conversation column + idempotency for system (bot) outbound messages.
-- 'none' (default) is today's behavior: the flow runtime never touches such a conversation. The column is
-- informational for the UI; "who may speak" is derived (automation_mode='bot' AND assigned_to_user_id IS NULL).
ALTER TABLE conversations ADD COLUMN automation_mode TEXT NOT NULL DEFAULT 'none'
  CHECK (automation_mode IN ('none','bot','waiting_human','human'));

-- The company ADR-0018 says a conversation is about (a contact may represent several): chosen by the contact, never
-- by "first link found". Set by the flow runtime (resolve_customer_context / customer_choice) and by nothing else here.
ALTER TABLE conversations ADD COLUMN active_customer_account_id UUID;
ALTER TABLE conversations ADD CONSTRAINT conversations_active_customer_account_fk
  FOREIGN KEY (tenant_id, active_customer_account_id) REFERENCES customer_accounts(tenant_id, id);

-- messages_outbound_idempotency_uq (000025) keys on sent_by_user_id, and NULLs never collide in a unique index, so a
-- system sender (NULL) had no protection against duplicates. Bot sends use the key 'flow:<run>:<seq>'.
CREATE UNIQUE INDEX messages_system_idempotency_uq ON messages(tenant_id, idempotency_key)
  WHERE sent_by_user_id IS NULL AND idempotency_key IS NOT NULL;
