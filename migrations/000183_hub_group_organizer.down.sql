DROP TABLE IF EXISTS hub_tenant_relationships;
DROP TABLE IF EXISTS hub_group_tenant_links;
DROP TABLE IF EXISTS hub_group_catalog;

ALTER TABLE hub_tenant_service_contracts DROP CONSTRAINT hub_tenant_service_contracts_management_scopes_check;
ALTER TABLE hub_tenant_service_contracts
  ADD CONSTRAINT hub_tenant_service_contracts_management_scopes_check
    CHECK (management_scopes <@ ARRAY['channels', 'integrations', 'team', 'queues', 'settings']::TEXT[]);
