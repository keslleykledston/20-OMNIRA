DELETE FROM role_permissions WHERE permission_key = 'conversation.read_internal';
DELETE FROM permissions WHERE key = 'conversation.read_internal';
