-- ADR-0040 phase 02: the CORE of delegated serving. Nothing here changes what any existing policy or route allows (members are untouched);
-- no row-level policy calls these functions yet (phase 03 attaches them, per domain). It defines, in ONE place each:
--   * what a Hub may delegate to the people it serves an instance with (the contract's CEILING, `delegable_permissions`);
--   * what one person was granted (`effective_access_grants.permissions`, always read together with the ceiling: grant AND contract);
--   * which permission keys can ever be delegated, and to which data domain they give access (`permission_domains`, data, not code):
--     a key with no row there is NEVER delegable (team, contract, credentials, AI keys, security... have none, by construction);
--   * the explicit ACTING CONTEXT: a request acts either as a member or as a Hub agent for ONE hub, never as the sum of both.
--     `lock_served_tenant` is the only door that puts a request in the delegated context (it validates, pins the rows, then sets
--     app.acting_hub for the transaction); `actor_has_permission` and `has_delegated_access` answer from that context alone.
-- Every condition is evaluated at call time (now()), like has_active_hub_access / has_hub_manage_access: nothing is copied, so a
-- revocation, a suspension or a shrunk ceiling takes effect on the very next call.

ALTER TABLE hub_tenant_service_contracts ADD COLUMN delegable_permissions TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE effective_access_grants ADD COLUMN permissions TEXT[] NOT NULL DEFAULT '{}';

-- New KEYS are data, as the ADR says; no role (member) receives them, so members' permissions are unchanged. They are the READ side of
-- what today is implied by "can attend": a delegated agent needs them to read, a member never asks for them.
INSERT INTO permissions (key, description) VALUES
  ('conversation.read',  'Read the conversations and messages of an instance (delegated serving)'),
  ('conversation.reply', 'Reply in the conversations of an instance (delegated serving)'),
  ('media.read',         'Open the attachments of an instance (delegated serving)'),
  ('contact.read',       'Read the contact card and its customer links (delegated serving)')
ON CONFLICT (key) DO NOTHING;

CREATE TABLE permission_domains (
  permission_key TEXT PRIMARY KEY REFERENCES permissions (key) ON DELETE CASCADE,
  domain         TEXT NOT NULL CHECK (domain IN ('conversation', 'media', 'contact', 'ticket', 'flow', 'ai')),
  need           TEXT NOT NULL CHECK (need IN ('read', 'write'))
);
-- 'flow' and 'ai' are reserved: no key maps to them until phase 05.
INSERT INTO permission_domains (permission_key, domain, need) VALUES
  ('conversation.read',  'conversation', 'read'),
  ('conversation.reply', 'conversation', 'write'),
  ('conversation.claim', 'conversation', 'write'),
  ('media.read',         'media',        'read'),
  ('contact.read',       'contact',      'read'),
  ('account.read',       'contact',      'read'),
  ('contact.classify',   'contact',      'write'),
  ('ticket.read',        'ticket',       'read'),
  ('topic.read',         'ticket',       'read'),
  ('ticket.create',      'ticket',       'write'),
  ('ticket.update',      'ticket',       'write'),
  ('topic.manage',       'ticket',       'write');
ALTER TABLE permission_domains ENABLE ROW LEVEL SECURITY;
ALTER TABLE permission_domains FORCE ROW LEVEL SECURITY;
CREATE POLICY permission_domains_read ON permission_domains FOR SELECT USING (true);
CREATE POLICY permission_domains_write ON permission_domains FOR ALL USING (is_system_admin()) WITH CHECK (is_system_admin());
GRANT SELECT ON permission_domains TO omnira_app;

-- The permission keys this person may use in this instance through THIS hub, right now: the grant's keys that the contract's ceiling also
-- holds and that are delegable at all. Empty when anything about the relationship is not live (account, hub, instance, contract, hub
-- membership, individual grant). The hub is mandatory: serving is always for one named hub. A hub admin needs a grant like anybody else.
CREATE OR REPLACE FUNCTION delegated_permissions(p_tenant_id UUID, p_user_id UUID, p_hub_id UUID) RETURNS TEXT[] AS $$
  SELECT COALESCE((
    SELECT ARRAY(
             SELECT DISTINCT k FROM unnest(g.permissions) k
             WHERE k = ANY (c.delegable_permissions)
               AND EXISTS (SELECT 1 FROM public.permission_domains d WHERE d.permission_key = k)
             ORDER BY k)
    FROM public.hub_tenant_service_contracts c
    JOIN public.service_hubs h ON h.id = c.hub_id
    JOIN public.tenants t ON t.id = c.tenant_id
    JOIN public.hub_memberships hm ON hm.hub_id = c.hub_id AND hm.user_id = p_user_id
    JOIN public.effective_access_grants g ON g.service_contract_id = c.id AND g.hub_id = c.hub_id AND g.tenant_id = c.tenant_id AND g.user_id = p_user_id
    WHERE c.tenant_id = p_tenant_id AND c.hub_id = p_hub_id
      AND (p_user_id = public.current_user_id() OR public.is_system_admin())
      AND public.user_is_active(p_user_id)
      AND t.status = 'active' AND h.status = 'active'
      AND c.status = 'active' AND c.valid_from <= now() AND (c.valid_until IS NULL OR c.valid_until > now())
      AND g.status = 'active' AND g.valid_from <= now() AND (g.valid_until IS NULL OR g.valid_until > now())
    LIMIT 1), '{}'::TEXT[]);
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION delegated_permissions(UUID, UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION delegated_permissions(UUID, UUID, UUID) TO omnira_app;

-- The only door into the delegated acting context. Like lock_managed_tenant: refuse unless the relationship is live and delegates at least one
-- key, pin every row it rests on for the rest of the transaction (a suspension / revocation / contract change waits for this request),
-- re-check after the locks, and ONLY THEN set the acting hub for this transaction.
CREATE OR REPLACE FUNCTION lock_served_tenant(p_tenant_id UUID, p_user_id UUID, p_hub_id UUID) RETURNS BOOLEAN AS $$
BEGIN
  IF NOT (p_user_id = public.current_user_id() OR public.is_system_admin()) THEN
    RETURN false;
  END IF;
  IF cardinality(public.delegated_permissions(p_tenant_id, p_user_id, p_hub_id)) = 0 THEN
    RETURN false;
  END IF;
  PERFORM 1 FROM public.tenants WHERE id = p_tenant_id FOR SHARE;
  PERFORM 1 FROM public.service_hubs WHERE id = p_hub_id FOR SHARE;
  PERFORM 1 FROM public.hub_tenant_service_contracts WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id FOR SHARE;
  PERFORM 1 FROM public.hub_memberships WHERE hub_id = p_hub_id AND user_id = p_user_id FOR SHARE;
  PERFORM 1 FROM public.users WHERE id = p_user_id FOR SHARE;
  PERFORM 1 FROM public.effective_access_grants WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id AND user_id = p_user_id FOR SHARE;
  IF cardinality(public.delegated_permissions(p_tenant_id, p_user_id, p_hub_id)) = 0 THEN
    RETURN false;
  END IF;
  PERFORM set_config('app.acting_hub', p_hub_id::TEXT, true);
  RETURN true;
END;
$$ LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION lock_served_tenant(UUID, UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION lock_served_tenant(UUID, UUID, UUID) TO omnira_app;

-- "May this person do <permission> in this instance, in the context this request acts in?" — the ONE definition. NO UNION of contexts:
--   not acting for a hub  -> only the member's role permissions (exactly what the modules ask today);
--   acting for a hub      -> only delegated_permissions of that hub; a membership of the same person contributes nothing.
CREATE OR REPLACE FUNCTION actor_has_permission(p_tenant_id UUID, p_user_id UUID, p_permission TEXT) RETURNS BOOLEAN AS $$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND CASE
           WHEN NULLIF(current_setting('app.acting_hub', true), '') IS NULL THEN
             EXISTS (SELECT 1 FROM public.memberships m JOIN public.role_permissions rp ON rp.role_id = m.role_id
                     WHERE m.tenant_id = p_tenant_id AND m.user_id = p_user_id AND m.status = 'active' AND rp.permission_key = p_permission)
           ELSE p_permission = ANY (public.delegated_permissions(p_tenant_id, p_user_id, NULLIF(current_setting('app.acting_hub', true), '')::UUID))
         END;
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION actor_has_permission(UUID, UUID, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION actor_has_permission(UUID, UUID, TEXT) TO omnira_app;

-- The predicate the row-level policies of phase 03 will use for delegated access to a data DOMAIN: true only in the delegated context, and
-- only if a delegated key maps to that domain at the needed level (write implies read inside a domain).
CREATE OR REPLACE FUNCTION has_delegated_access(p_tenant_id UUID, p_user_id UUID, p_domain TEXT, p_need TEXT) RETURNS BOOLEAN AS $$
  SELECT NULLIF(current_setting('app.acting_hub', true), '') IS NOT NULL
     AND EXISTS (
       SELECT 1
       FROM unnest(public.delegated_permissions(p_tenant_id, p_user_id, NULLIF(current_setting('app.acting_hub', true), '')::UUID)) k
       JOIN public.permission_domains d ON d.permission_key = k
       WHERE d.domain = p_domain AND (d.need = 'write' OR p_need = 'read'));
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION has_delegated_access(UUID, UUID, TEXT, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION has_delegated_access(UUID, UUID, TEXT, TEXT) TO omnira_app;

-- The audit repository inserts with ON CONFLICT (id) DO NOTHING, and PostgreSQL then also evaluates the SELECT policy of the row being
-- written (the same reason as audit_events_hub_manage_read_own in 000103). A Hub agent attending an instance has no membership there, so
-- without this their audited action would be refused as a whole. Deliberately narrow: only the agent's OWN events of an instance they
-- are serving right now, in the delegated context; never the instance's trail in general.
CREATE POLICY audit_events_hub_serve_read_own ON audit_events FOR SELECT
  USING (tenant_id IS NOT NULL AND actor_id = current_user_id()
         AND cardinality(delegated_permissions(tenant_id, current_user_id(), NULLIF(current_setting('app.acting_hub', true), '')::UUID)) > 0);
