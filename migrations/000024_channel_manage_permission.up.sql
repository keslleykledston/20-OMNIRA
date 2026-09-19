-- W1: permission for managing WhatsApp channel connections/sessions.
-- Admin-only, matching the has_active_admin_membership write policy on
-- channel_connections / channel_credentials.
INSERT INTO permissions (key, description) VALUES
  ('channel.manage', 'Create and operate WhatsApp channel connections and sessions')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_key)
SELECT r.id, 'channel.manage' FROM roles r
WHERE r.tenant_id IS NULL AND r.key = 'tenant_admin'
ON CONFLICT DO NOTHING;
