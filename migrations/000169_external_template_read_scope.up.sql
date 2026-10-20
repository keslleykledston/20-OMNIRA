-- ADR-0052: new scope `template.read` for the external API — list the company's APPROVED, sendable WhatsApp templates
-- (channel_message_templates, already synced from Meta by channel.manage). Read-only; no new table.
SET LOCAL lock_timeout = '5s';

ALTER TABLE external_app_installations DROP CONSTRAINT external_app_installations_scopes_check;
ALTER TABLE external_app_installations ADD CONSTRAINT external_app_installations_scopes_check
  CHECK (cardinality(scopes) BETWEEN 1 AND 18
     AND scopes <@ ARRAY['conversation.read', 'message.read', 'media.read', 'contact.read', 'events.read',
                         'conversation.claim', 'conversation.transfer', 'conversation.finalize', 'message.send', 'service.read', 'template.read']::TEXT[]);
