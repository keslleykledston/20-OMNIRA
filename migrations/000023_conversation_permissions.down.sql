-- Undo only the permissions created by 000023.
DELETE FROM role_permissions WHERE permission_key IN ('conversation.claim', 'conversation.manage');
DELETE FROM permissions WHERE key IN ('conversation.claim', 'conversation.manage');
