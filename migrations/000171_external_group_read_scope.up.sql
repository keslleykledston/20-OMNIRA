-- ADR-0051: external API read-only access to WhatsApp groups (CRM pendency P1 — kind/is_group/jid, participants, group message sender).
-- No new tables: wa_groups/wa_group_messages already exist (ADR-0015). This migration only adds the new closed-list scope.
SET LOCAL lock_timeout = '5s';

ALTER TABLE external_app_installations DROP CONSTRAINT external_app_installations_scopes_check;
ALTER TABLE external_app_installations ADD CONSTRAINT external_app_installations_scopes_check
  CHECK (cardinality(scopes) BETWEEN 1 AND 19
     AND scopes <@ ARRAY['conversation.read', 'message.read', 'media.read', 'contact.read', 'events.read',
                         'conversation.claim', 'conversation.transfer', 'conversation.finalize', 'message.send', 'service.read', 'template.read', 'message.note',
                         'group.read.external']::TEXT[]);
