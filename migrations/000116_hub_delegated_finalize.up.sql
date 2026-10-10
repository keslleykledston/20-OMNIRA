-- ADR-0040 phase 05b: a Hub agent may FINALIZE (close) the attendance of a conversation THEY HOLD (key conversation.finalize, data domain `conversation`, write).
-- Optional per grant, in no preset that exists before this one. Finalizing writes the conversation, its local tickets, the closure and its follow-ups, so it goes
-- through the Hub's own write path (ADR-0037): authorize in the caller's RLS session, execute in a tenant-scoped system session that re-proves the delegation
-- inside the transaction; no policy opens any of those tables for writing to a delegate.
INSERT INTO permissions (key, description) VALUES
  ('conversation.finalize', 'Finalize the attendance of a conversation you hold (delegated serving)')
ON CONFLICT (key) DO NOTHING;
INSERT INTO permission_domains (permission_key, domain, need) VALUES ('conversation.finalize', 'conversation', 'write');
