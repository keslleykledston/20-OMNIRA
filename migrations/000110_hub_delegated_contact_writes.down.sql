DROP FUNCTION IF EXISTS delegated_dequeue_spam(UUID, UUID);
DROP FUNCTION IF EXISTS delegated_recompute_contact_kinds(UUID, UUID);
DROP POLICY IF EXISTS customer_accounts_read_delegated ON customer_accounts;
DROP POLICY IF EXISTS contact_account_links_update_delegated ON contact_account_links;
DROP POLICY IF EXISTS contact_account_links_insert_delegated ON contact_account_links;
DROP POLICY IF EXISTS contact_account_links_read_delegated ON contact_account_links;
DROP POLICY IF EXISTS contacts_update_delegated ON contacts;
DROP POLICY IF EXISTS messages_acting_hub_only ON messages;
DROP POLICY IF EXISTS conversations_acting_hub_only ON conversations;
