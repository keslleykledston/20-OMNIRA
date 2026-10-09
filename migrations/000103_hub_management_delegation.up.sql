-- ADR-0038 phase 3: a Hub may MANAGE (not only read or reply in) the channels / integrations of the instances it serves.
--
--   * the CONTRACT says which kinds of management it delegates (`management_scopes`): nothing by default;
--   * a GRANT says which agent may manage (`can_manage`): never implied by can_reply; an admin of the hub is eligible without a grant;
--   * has_hub_manage_access(...) is the ONE definition of "this user may manage this tenant through a Hub", used by the row-level
--     policies below, so the database (not only the application) refuses everything else. The system-session shortcut was
--     rejected on purpose: it would switch RLS off for every tenant.
--
-- Scopes in use today: 'channels' (WhatsApp lines) and 'integrations' (ERP/CRM connections). 'team', 'queues' and 'settings' are
-- reserved names; nothing reads them yet.
ALTER TABLE hub_tenant_service_contracts
  ADD COLUMN management_scopes TEXT[] NOT NULL DEFAULT '{}'
    CHECK (management_scopes <@ ARRAY['channels', 'integrations', 'team', 'queues', 'settings']::TEXT[]);

ALTER TABLE effective_access_grants
  ADD COLUMN can_manage BOOLEAN NOT NULL DEFAULT false;

-- p_scope NULL = "any delegated scope". Every condition is evaluated at query time (now()), like has_active_hub_access.
CREATE OR REPLACE FUNCTION has_hub_manage_access(p_tenant_id UUID, p_user_id UUID, p_scope TEXT DEFAULT NULL, p_hub_id UUID DEFAULT NULL)
RETURNS BOOLEAN AS $$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND EXISTS (
       SELECT 1
       FROM public.hub_tenant_service_contracts c
       JOIN public.service_hubs h ON h.id = c.hub_id
       JOIN public.tenants t ON t.id = c.tenant_id
       JOIN public.hub_memberships hm ON hm.hub_id = c.hub_id AND hm.user_id = p_user_id
       WHERE c.tenant_id = p_tenant_id
         AND (p_hub_id IS NULL OR c.hub_id = p_hub_id)
         AND t.status = 'active' AND h.status = 'active'
         AND c.status = 'active' AND c.valid_from <= now() AND (c.valid_until IS NULL OR c.valid_until > now())
         AND CASE WHEN p_scope IS NULL THEN cardinality(c.management_scopes) > 0 ELSE p_scope = ANY (c.management_scopes) END
         AND (
           EXISTS (SELECT 1 FROM public.roles r WHERE r.id = hm.role_id AND r.tenant_id IS NULL AND r.key = 'hub_admin')
           OR EXISTS (
             SELECT 1 FROM public.effective_access_grants g
             WHERE g.service_contract_id = c.id AND g.hub_id = c.hub_id AND g.user_id = p_user_id AND g.can_manage
               AND g.status = 'active' AND g.valid_from <= now() AND (g.valid_until IS NULL OR g.valid_until > now())
           )
         )
     );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

REVOKE EXECUTE ON FUNCTION has_hub_manage_access(UUID, UUID, TEXT, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION has_hub_manage_access(UUID, UUID, TEXT, UUID) TO omnira_app;

-- The scope a connection row falls under: ERP/CRM connections are 'integrations', everything else (WhatsApp lines) is 'channels'.
CREATE OR REPLACE FUNCTION channel_connection_scope(p_channel TEXT) RETURNS TEXT AS $$
  SELECT CASE WHEN p_channel = 'erp' THEN 'integrations' ELSE 'channels' END;
$$ LANGUAGE SQL IMMUTABLE;
REVOKE EXECUTE ON FUNCTION channel_connection_scope(TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION channel_connection_scope(TEXT) TO omnira_app;

-- Permissive policies are OR-ed with the member/admin policies of 000010: nothing existing changes for tenant users.
CREATE POLICY channel_connections_hub_manage_read ON channel_connections FOR SELECT
  USING (has_hub_manage_access(tenant_id, current_user_id(), channel_connection_scope(channel)));
CREATE POLICY channel_connections_hub_manage_insert ON channel_connections FOR INSERT
  WITH CHECK (has_hub_manage_access(tenant_id, current_user_id(), channel_connection_scope(channel)));
CREATE POLICY channel_connections_hub_manage_update ON channel_connections FOR UPDATE
  USING (has_hub_manage_access(tenant_id, current_user_id(), channel_connection_scope(channel)))
  WITH CHECK (has_hub_manage_access(tenant_id, current_user_id(), channel_connection_scope(channel)));
CREATE POLICY channel_connections_hub_manage_delete ON channel_connections FOR DELETE
  USING (has_hub_manage_access(tenant_id, current_user_id(), channel_connection_scope(channel)));

-- A credential follows its connection: it is visible to the caller only if the connection row is (so the scope is decided once).
CREATE POLICY channel_credentials_hub_manage_read ON channel_credentials FOR SELECT
  USING (EXISTS (SELECT 1 FROM channel_connections c WHERE c.id = connection_id AND c.tenant_id = channel_credentials.tenant_id));
CREATE POLICY channel_credentials_hub_manage_insert ON channel_credentials FOR INSERT
  WITH CHECK (EXISTS (SELECT 1 FROM channel_connections c WHERE c.id = connection_id AND c.tenant_id = channel_credentials.tenant_id
    AND has_hub_manage_access(c.tenant_id, current_user_id(), channel_connection_scope(c.channel))));
CREATE POLICY channel_credentials_hub_manage_update ON channel_credentials FOR UPDATE
  USING (EXISTS (SELECT 1 FROM channel_connections c WHERE c.id = connection_id AND c.tenant_id = channel_credentials.tenant_id
    AND has_hub_manage_access(c.tenant_id, current_user_id(), channel_connection_scope(c.channel))))
  WITH CHECK (EXISTS (SELECT 1 FROM channel_connections c WHERE c.id = connection_id AND c.tenant_id = channel_credentials.tenant_id
    AND has_hub_manage_access(c.tenant_id, current_user_id(), channel_connection_scope(c.channel))));
CREATE POLICY channel_credentials_hub_manage_delete ON channel_credentials FOR DELETE
  USING (EXISTS (SELECT 1 FROM channel_connections c WHERE c.id = connection_id AND c.tenant_id = channel_credentials.tenant_id
    AND has_hub_manage_access(c.tenant_id, current_user_id(), channel_connection_scope(c.channel))));

-- The company's capability switches (ADR-0038) must stay readable to whoever manages through the Hub. WITHOUT this policy the
-- checker would see "no row" and default to ON: a switched-off capability would silently stop being enforced for Hub managers.
CREATE POLICY tenant_entitlements_hub_manage_read ON tenant_entitlements FOR SELECT
  USING (has_hub_manage_access(tenant_id, current_user_id()));

-- Holds the company ACTIVE for the rest of the caller's transaction (a share lock on the tenants row, which the suspension UPDATE
-- waits for - the same rule as platformdb.LockTenantActive, ADR-0038) and says whether the caller may manage it through a Hub.
-- A definer function, because a Hub manager has no policy to lock a tenants row directly, and it answers false for anybody who is
-- not an eligible manager: it is not a way to probe or lock other companies. Volatile on purpose (a lock is a side effect).
CREATE OR REPLACE FUNCTION lock_managed_tenant(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT t.status = 'active'
  FROM public.tenants t
  WHERE t.id = p_tenant_id
    AND (p_user_id = public.current_user_id() OR public.is_system_admin())
    AND public.has_hub_manage_access(p_tenant_id, p_user_id)
  FOR SHARE OF t;
$$ LANGUAGE SQL VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION lock_managed_tenant(UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION lock_managed_tenant(UUID, UUID) TO omnira_app;

-- The audit repository inserts with ON CONFLICT (id) DO NOTHING, and PostgreSQL then also evaluates the SELECT policy of the row being
-- written. A Hub manager has no tenant membership, so without this the audit insert (and with it the whole management write) is refused.
-- Deliberately narrow: only the manager's OWN events of an instance they may manage, never the instance's trail in general.
CREATE POLICY audit_events_hub_manage_read_own ON audit_events FOR SELECT
  USING (tenant_id IS NOT NULL AND actor_id = current_user_id() AND has_hub_manage_access(tenant_id, current_user_id()));

-- The instances this person may manage through a hub, with the scopes they may use there. A hub admin without any grant cannot read
-- the tenants row under RLS (that policy is about delegated READING), so the name is read here, inside a definer function that can
-- only be asked about the caller themselves and only returns what has_hub_manage_access already allows.
CREATE OR REPLACE FUNCTION managed_instances(p_user_id UUID, p_hub_id UUID)
RETURNS TABLE (tenant_id UUID, name TEXT, scopes TEXT[]) AS $$
  SELECT t.id, COALESCE(NULLIF(t.trade_name, ''), t.legal_name),
         ARRAY(SELECT sc FROM unnest(c.management_scopes) sc WHERE public.has_hub_manage_access(t.id, p_user_id, sc, p_hub_id) ORDER BY sc)
  FROM public.hub_tenant_service_contracts c
  JOIN public.tenants t ON t.id = c.tenant_id
  WHERE c.hub_id = p_hub_id
    AND p_user_id = public.current_user_id()
    AND public.has_hub_manage_access(t.id, p_user_id, NULL, p_hub_id)
  ORDER BY lower(COALESCE(NULLIF(t.trade_name, ''), t.legal_name)), t.id;
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION managed_instances(UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION managed_instances(UUID, UUID) TO omnira_app;
