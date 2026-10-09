DROP POLICY IF EXISTS audit_events_hub_serve_read_own ON audit_events;
DROP FUNCTION IF EXISTS has_delegated_access(UUID, UUID, TEXT, TEXT);
DROP FUNCTION IF EXISTS actor_has_permission(UUID, UUID, TEXT);
DROP FUNCTION IF EXISTS lock_served_tenant(UUID, UUID, UUID);
DROP FUNCTION IF EXISTS delegated_permissions(UUID, UUID, UUID);
DROP TABLE IF EXISTS permission_domains;
DELETE FROM permissions WHERE key IN ('conversation.read', 'conversation.reply', 'media.read', 'contact.read');
ALTER TABLE effective_access_grants DROP COLUMN IF EXISTS permissions;
ALTER TABLE hub_tenant_service_contracts DROP COLUMN IF EXISTS delegable_permissions;
