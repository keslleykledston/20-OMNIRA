DELETE FROM role_permissions WHERE permission_key IN ('account.read','account.manage','contact.classify','identity.manage');
DELETE FROM permissions WHERE key IN ('account.read','account.manage','contact.classify','identity.manage');
DROP TABLE IF EXISTS account_external_links;
DROP TABLE IF EXISTS customer_accounts;
