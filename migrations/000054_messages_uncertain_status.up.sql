-- PILOT.4A2: a truthful terminal outcome for outbound messages whose
-- provider result could not be proven (transport ambiguity exhausted, or a
-- provider-returned message id that doesn't match the reserved id) — never
-- silently reported as the confirmed 'failed' state, which is reserved for
-- outcomes OMNIRA can actually prove. Does not touch provider_message_id
-- or reserved_provider_message_id semantics, indexes, or RLS.
ALTER TABLE messages DROP CONSTRAINT messages_status_check;
ALTER TABLE messages ADD CONSTRAINT messages_status_check
  CHECK (status IN ('received', 'queued', 'sent', 'delivered', 'read', 'failed', 'uncertain'));
