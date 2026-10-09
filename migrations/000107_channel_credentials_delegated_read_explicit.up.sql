-- ADR-0040 section 5 (external architecture review, 2026-10-09): the delegated READ of a credential stated no condition of its own.
-- 000103 wrote it as "visible only if the parent connection row is", so the scope was decided once, in the connection policy, through an
-- EXISTS that PostgreSQL evaluates under the connection's RLS. The result was right, but it rested on an indirect effect: changing the
-- connection read policy would have changed who reads credentials. The read now states the same condition as the other three
-- delegated policies of this table (insert / update / delete): the manager's grant must hold the scope of THAT connection's channel.
-- Nothing changes for members or admins (their policies are untouched) and nothing changes for Hub managers today.
DROP POLICY IF EXISTS channel_credentials_hub_manage_read ON channel_credentials;
CREATE POLICY channel_credentials_hub_manage_read ON channel_credentials FOR SELECT
  USING (EXISTS (SELECT 1 FROM channel_connections c WHERE c.id = connection_id AND c.tenant_id = channel_credentials.tenant_id
    AND has_hub_manage_access(c.tenant_id, current_user_id(), channel_connection_scope(c.channel))));
