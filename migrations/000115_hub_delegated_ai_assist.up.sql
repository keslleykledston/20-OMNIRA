-- ADR-0040 phase 05: a Hub agent may ask for the AI SUMMARY of a conversation they can see (key ai.assist). One new key, optional per grant, and NOT in any
-- preset that exists: the conversation text leaves the instance for the platform's AI provider, so it is a deliberate choice of the contract (ceiling) and of
-- the person (grant). The data domain is `ai` (reserved since 108) with READ: no policy uses the domain, the key only has to be delegable. What the summary
-- reads is what the person can already read (conversation and messages through the delegated policies, queue scope included); nothing new is opened.
INSERT INTO permissions (key, description) VALUES
  ('ai.assist', 'Ask the AI for a summary of a conversation you can see (delegated serving)')
ON CONFLICT (key) DO NOTHING;
INSERT INTO permission_domains (permission_key, domain, need) VALUES ('ai.assist', 'ai', 'read');
-- NOTE (Codex review of phase 05, HIGH): the database does NOT ask for conversation.read on conversations/messages, by design (ADR-0040 4.1: the database answers
-- by relationship, instance and queue scope, the KEY is asked by the route/handler; contact.read or media.read alone legitimately read the contact or the file of a
-- visible conversation). ai.assist therefore asks for conversation.read itself, in the handler (internal/ai/adapters/http.go), before anything is read or sent.
