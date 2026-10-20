SET LOCAL lock_timeout = '5s';

UPDATE external_app_installations SET scopes = array_remove(scopes, 'group.read.external') WHERE 'group.read.external' = ANY (scopes) AND cardinality(scopes) > 1;
UPDATE external_app_installations SET scopes = ARRAY['events.read'], status = CASE WHEN status = 'revoked' THEN status ELSE 'suspended' END,
       suspended_at = CASE WHEN status = 'revoked' THEN suspended_at ELSE COALESCE(suspended_at, now()) END
 WHERE 'group.read.external' = ANY (scopes);

ALTER TABLE external_app_installations DROP CONSTRAINT external_app_installations_scopes_check;
ALTER TABLE external_app_installations ADD CONSTRAINT external_app_installations_scopes_check
  CHECK (cardinality(scopes) BETWEEN 1 AND 19
     AND scopes <@ ARRAY['conversation.read', 'message.read', 'media.read', 'contact.read', 'events.read',
                         'conversation.claim', 'conversation.transfer', 'conversation.finalize', 'message.send', 'service.read', 'template.read', 'message.note']::TEXT[]);
