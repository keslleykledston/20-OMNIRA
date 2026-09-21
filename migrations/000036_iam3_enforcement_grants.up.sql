-- M06: IAM3 Role-Permission Grants + Enforcement

-- Add hub_admin permissions (hub.read, hub.manage)
-- hub_admin role was created in 000002 but had no permissions.
-- In IAM3 MVP, hub_admin does NOT receive tenant-scoped permissions.
INSERT INTO role_permissions (role_id, permission_key)
SELECT r.id, p.key FROM roles r, permissions p
WHERE r.key = 'hub_admin' AND r.tenant_id IS NULL
  AND p.key IN ('hub.read', 'hub.manage')
ON CONFLICT DO NOTHING;

-- Note: system_admin continues to use app.is_system_admin GUC bypass.
-- No DB permissions are assigned to system_admin in this wave.
