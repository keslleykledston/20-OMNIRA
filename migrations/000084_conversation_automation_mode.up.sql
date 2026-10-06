-- FLOW.3 (ADR-0019): additive conversation column + idempotency for system (bot) outbound messages.
-- The company a flow is talking about is NOT stored on the conversation (ADR-0017/0018: one conversation can carry several
-- subjects, each with its own company). It lives on flow_runs.active_customer_account_id and on the ticket's customer_account_id.
-- 'none' (default) is today's behavior: the flow runtime never touches such a conversation. The column is
-- informational for the UI; "who may speak" is derived (automation_mode='bot' AND assigned_to_user_id IS NULL).
ALTER TABLE conversations ADD COLUMN automation_mode TEXT NOT NULL DEFAULT 'none'
  CHECK (automation_mode IN ('none','bot','waiting_human','human'));

-- messages_outbound_idempotency_uq (000025) keys on sent_by_user_id, and NULLs never collide in a unique index, so a
-- system sender (NULL) had no protection against duplicates. Bot sends use the key 'flow:<run>:<seq>'.
CREATE UNIQUE INDEX messages_system_idempotency_uq ON messages(tenant_id, idempotency_key)
  WHERE sent_by_user_id IS NULL AND idempotency_key IS NOT NULL;
