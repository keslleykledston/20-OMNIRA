-- Undo only what 000024 created.
DELETE FROM role_permissions WHERE permission_key = 'channel.manage';
DELETE FROM permissions WHERE key = 'channel.manage';
