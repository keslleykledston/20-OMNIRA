DELETE FROM permission_domains WHERE permission_key = 'conversation.finalize';
DELETE FROM role_permissions WHERE permission_key = 'conversation.finalize';
DELETE FROM permissions WHERE key = 'conversation.finalize';
