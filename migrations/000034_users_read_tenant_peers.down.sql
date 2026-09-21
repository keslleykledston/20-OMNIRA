DROP POLICY IF EXISTS user_identities_update_system ON user_identities;
DROP POLICY IF EXISTS user_identities_insert_system ON user_identities;
DROP POLICY IF EXISTS user_identities_read ON user_identities;
ALTER TABLE user_identities DISABLE ROW LEVEL SECURITY;
GRANT DELETE ON user_identities TO omnira_app;

DROP POLICY IF EXISTS users_read_tenant_peers ON users;
DROP FUNCTION IF EXISTS can_read_tenant_peer(UUID);
