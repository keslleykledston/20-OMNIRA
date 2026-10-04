DELETE FROM role_permissions WHERE permission_key IN ('group.read','group.manage');
DELETE FROM permissions WHERE key IN ('group.read','group.manage');
