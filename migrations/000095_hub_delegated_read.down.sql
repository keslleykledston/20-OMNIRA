-- Rollback ADR-0026: Hub delegated READ policies
DROP POLICY IF EXISTS messages_read_hub_delegation ON messages;
DROP POLICY IF EXISTS conversations_read_hub_delegation ON conversations;
DROP POLICY IF EXISTS tenants_read_hub_delegation ON tenants;
