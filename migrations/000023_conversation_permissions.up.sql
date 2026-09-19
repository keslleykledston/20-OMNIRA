-- M05.4: RBAC permissions for manual conversation assignment.
--   conversation.claim  : assign a conversation to oneself / release one's own
--   conversation.manage : assign to another agent / release someone else's
INSERT INTO permissions (key, description) VALUES
  ('conversation.claim', 'Claim and release own conversations'),
  ('conversation.manage', 'Assign and unassign conversations for other agents')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_key)
SELECT r.id, p.key FROM roles r, permissions p
WHERE r.tenant_id IS NULL
  AND r.key IN ('tenant_admin', 'tenant_supervisor', 'tenant_agent')
  AND p.key = 'conversation.claim'
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_key)
SELECT r.id, p.key FROM roles r, permissions p
WHERE r.tenant_id IS NULL
  AND r.key IN ('tenant_admin', 'tenant_supervisor')
  AND p.key = 'conversation.manage'
ON CONFLICT DO NOTHING;
