-- OPERATIONAL GATE: take a backup and get the owner's approval first. DATA LOSS: drops every external application installation, its
-- credentials and allow-lists (the applications stop authenticating). Refuses while any installation is still alive, so a live CRM is never
-- cut off by a forgotten rollback: revoke the installations (they drain their conversations) first.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM external_app_installations WHERE status <> 'revoked') THEN
    RAISE EXCEPTION 'external_app_installations still has active or suspended installations; revoke them before rolling this migration back';
  END IF;
END $$;
DROP FUNCTION IF EXISTS external_installation_allowlists(UUID, UUID);
DROP FUNCTION IF EXISTS external_recheck(UUID, UUID, UUID);
DROP FUNCTION IF EXISTS external_credential_touch(UUID, UUID, UUID);
DROP FUNCTION IF EXISTS external_credential_lookup(TEXT);
DROP TABLE IF EXISTS external_app_credentials;
DROP TABLE IF EXISTS external_app_installation_channels;
DROP TABLE IF EXISTS external_app_installation_queues;
DROP TABLE IF EXISTS external_app_installations;
DELETE FROM role_permissions WHERE permission_key IN ('integration.read', 'integration.manage');
DELETE FROM permissions WHERE key IN ('integration.read', 'integration.manage');
