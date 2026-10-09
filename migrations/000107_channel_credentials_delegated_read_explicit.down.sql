-- Back to the 000103 form: the credential is visible when its parent connection row is.
DROP POLICY IF EXISTS channel_credentials_hub_manage_read ON channel_credentials;
CREATE POLICY channel_credentials_hub_manage_read ON channel_credentials FOR SELECT
  USING (EXISTS (SELECT 1 FROM channel_connections c WHERE c.id = connection_id AND c.tenant_id = channel_credentials.tenant_id));
