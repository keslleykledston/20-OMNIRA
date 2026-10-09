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

-- An account that is not active manages (and answers) nothing through a Hub, whatever rows it still has (Codex review). Two definer functions,
-- because the users table is not readable by an arbitrary session, and deliberately NOT one general-purpose predicate:
--   * user_is_active(user)         internal helper of the other definer functions below and in 000105; NOT executable by the application role,
--                                  so no session can use it to ask about somebody else's account;
--   * session_account_active(sid)  the only door for session resolution: it answers about the owner of ONE session id (knowing the id is
--                                  already what makes a session resolvable), never about a user id.
CREATE OR REPLACE FUNCTION user_is_active(p_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT EXISTS (SELECT 1 FROM public.users WHERE id = p_user_id AND status = 'active');
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION user_is_active(UUID) FROM PUBLIC;

CREATE OR REPLACE FUNCTION session_account_active(p_session_id TEXT) RETURNS BOOLEAN AS $$
  SELECT EXISTS (SELECT 1 FROM public.auth_sessions s JOIN public.users u ON u.id = s.user_id WHERE s.id = p_session_id AND u.status = 'active');
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION session_account_active(TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION session_account_active(TEXT) TO omnira_app;

-- p_scope NULL = "any delegated scope". Every condition is evaluated at query time (now()), like has_active_hub_access.
CREATE OR REPLACE FUNCTION has_hub_manage_access(p_tenant_id UUID, p_user_id UUID, p_scope TEXT DEFAULT NULL, p_hub_id UUID DEFAULT NULL)
RETURNS BOOLEAN AS $$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND public.user_is_active(p_user_id)
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

-- Holds EVERYTHING the person's authority depends on until the caller's transaction ends, and says whether they may manage the instance
-- through this hub: the company (a suspension waits for the request, like every write path, ADR-0038), the hub, the contract, the person's
-- hub membership and account, and their grant. Revoking the grant, ending the contract, removing the person or pausing the hub each UPDATE or
-- DELETE one of those rows, so the change either commits before the answer below or waits until the request - including the call to the
-- channel provider - is over (Codex review: a revocation must not race an external call). The order is the one the administrative paths take
-- (company, hub, contract, membership, user, grant); one statement per table (a JOIN here would let PostgreSQL's re-check drop the row).
-- A definer function, because a hub manager has no policy to lock these rows directly. Strangers are answered BEFORE anything is locked, so it
-- is not a way to hold other companies' rows. Volatile on purpose (a lock is a side effect).
CREATE OR REPLACE FUNCTION lock_managed_tenant(p_tenant_id UUID, p_user_id UUID, p_hub_id UUID) RETURNS BOOLEAN AS $$
BEGIN
  IF NOT (p_user_id = public.current_user_id() OR public.is_system_admin()) THEN
    RETURN false;
  END IF;
  IF NOT public.has_hub_manage_access(p_tenant_id, p_user_id, NULL, p_hub_id) THEN
    RETURN false;
  END IF;
  PERFORM 1 FROM public.tenants WHERE id = p_tenant_id FOR SHARE;
  PERFORM 1 FROM public.service_hubs WHERE id = p_hub_id FOR SHARE;
  PERFORM 1 FROM public.hub_tenant_service_contracts WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id FOR SHARE;
  PERFORM 1 FROM public.hub_memberships WHERE hub_id = p_hub_id AND user_id = p_user_id FOR SHARE;
  PERFORM 1 FROM public.users WHERE id = p_user_id FOR SHARE;
  PERFORM 1 FROM public.effective_access_grants WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id AND user_id = p_user_id FOR SHARE;
  RETURN public.has_hub_manage_access(p_tenant_id, p_user_id, NULL, p_hub_id);
END;
$$ LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION lock_managed_tenant(UUID, UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION lock_managed_tenant(UUID, UUID, UUID) TO omnira_app;

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
