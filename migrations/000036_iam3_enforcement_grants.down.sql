-- Undo IAM3 enforcement grants

DELETE FROM role_permissions
WHERE role_id = (SELECT id FROM roles WHERE key = 'hub_admin' AND tenant_id IS NULL)
  AND permission_key IN ('hub.read', 'hub.manage');
