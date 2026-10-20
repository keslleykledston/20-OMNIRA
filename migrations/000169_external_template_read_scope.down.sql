SET LOCAL lock_timeout = '5s';

-- an installation never disappears with this rollback: the scope is taken away; one whose ONLY scope it was keeps events.read and is suspended
-- (it can do nothing until an administrator reviews it) — same pattern as 000164's rollback of service.read.
UPDATE external_app_installations SET scopes = array_remove(scopes, 'template.read') WHERE 'template.read' = ANY (scopes) AND cardinality(scopes) > 1;
UPDATE external_app_installations SET scopes = ARRAY['events.read'], status = CASE WHEN status = 'revoked' THEN status ELSE 'suspended' END,
       suspended_at = CASE WHEN status = 'revoked' THEN suspended_at ELSE COALESCE(suspended_at, now()) END
 WHERE 'template.read' = ANY (scopes);

ALTER TABLE external_app_installations DROP CONSTRAINT external_app_installations_scopes_check;
ALTER TABLE external_app_installations ADD CONSTRAINT external_app_installations_scopes_check
  CHECK (cardinality(scopes) BETWEEN 1 AND 17
     AND scopes <@ ARRAY['conversation.read', 'message.read', 'media.read', 'contact.read', 'events.read',
                         'conversation.claim', 'conversation.transfer', 'conversation.finalize', 'message.send', 'service.read']::TEXT[]);
