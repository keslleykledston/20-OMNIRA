-- ADR-0040 phase 03 (pilot): the DATA LAYER starts to answer the delegated context, for READING conversations, messages, media and the
-- contact card. Nothing is opened for writing here: claiming and replying keep going through the Hub's own, already reviewed path.
--
-- 1. ONE context at a time, in the database too. A request acting for a hub (app.acting_hub, set only by lock_served_tenant) is not a member:
--    the member predicates answer false there. Without this a person who is both would read through their membership what the grant does not give.
--    Requests that do not act for a hub (everything that exists today) are unchanged: the new clause is true when the setting is empty/absent.
CREATE OR REPLACE FUNCTION has_active_membership(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND NULLIF(current_setting('app.acting_hub', true), '') IS NULL
     AND EXISTS (
       SELECT 1 FROM public.memberships
       WHERE tenant_id = p_tenant_id AND user_id = p_user_id AND status = 'active'
     );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

CREATE OR REPLACE FUNCTION has_active_admin_membership(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND NULLIF(current_setting('app.acting_hub', true), '') IS NULL
     AND EXISTS (
       SELECT 1 FROM public.memberships m
       JOIN public.roles r ON m.role_id = r.id
       WHERE m.tenant_id = p_tenant_id AND m.user_id = p_user_id
         AND m.status = 'active' AND r.key = 'tenant_admin'
     );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

-- 1b. Reading the acting hub safely (Codex review, LOW): app.acting_hub is a text setting the application role can write, so a value that is not a UUID
--     must mean "no valid acting context", never a cast error. acting_hub() is the one place that reads it as a UUID; every delegated predicate uses it.
CREATE OR REPLACE FUNCTION acting_hub() RETURNS UUID AS $$
  SELECT CASE WHEN v ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN v::UUID END
  FROM (SELECT NULLIF(current_setting('app.acting_hub', true), '') AS v) s;
$$ LANGUAGE SQL STABLE SET search_path = pg_catalog, public, pg_temp;

CREATE OR REPLACE FUNCTION actor_has_permission(p_tenant_id UUID, p_user_id UUID, p_permission TEXT) RETURNS BOOLEAN AS $$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND CASE
           WHEN NULLIF(current_setting('app.acting_hub', true), '') IS NULL THEN
             EXISTS (SELECT 1 FROM public.memberships m JOIN public.role_permissions rp ON rp.role_id = m.role_id
                     WHERE m.tenant_id = p_tenant_id AND m.user_id = p_user_id AND m.status = 'active' AND rp.permission_key = p_permission)
           ELSE COALESCE(p_permission = ANY (public.delegated_permissions(p_tenant_id, p_user_id, public.acting_hub())), false)
         END;
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

CREATE OR REPLACE FUNCTION has_delegated_access(p_tenant_id UUID, p_user_id UUID, p_domain TEXT, p_need TEXT) RETURNS BOOLEAN AS $$
  SELECT public.acting_hub() IS NOT NULL
     AND EXISTS (
       SELECT 1
       FROM unnest(public.delegated_permissions(p_tenant_id, p_user_id, public.acting_hub())) k
       JOIN public.permission_domains d ON d.permission_key = k
       WHERE d.domain = p_domain AND (d.need = 'write' OR p_need = 'read'));
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

DROP POLICY IF EXISTS audit_events_hub_serve_read_own ON audit_events;
CREATE POLICY audit_events_hub_serve_read_own ON audit_events FOR SELECT
  USING (tenant_id IS NOT NULL AND actor_id = current_user_id()
         AND cardinality(delegated_permissions(tenant_id, current_user_id(), public.acting_hub())) > 0);

-- 2. The instances the CURRENT request may use, for one data domain at one level, in the delegated context. An uncorrelated list, so the
--    policies below evaluate it ONCE per statement (a hashed sub-plan) instead of once per row. Answers nothing outside the delegated context.
CREATE OR REPLACE FUNCTION delegated_tenants(p_domain TEXT, p_need TEXT) RETURNS SETOF UUID AS $$
  SELECT c.tenant_id
  FROM public.hub_tenant_service_contracts c
  WHERE public.acting_hub() IS NOT NULL
    AND c.hub_id = public.acting_hub()
    AND public.has_delegated_access(c.tenant_id, public.current_user_id(), p_domain, p_need);
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION delegated_tenants(TEXT, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION delegated_tenants(TEXT, TEXT) TO omnira_app;

-- 3. Read policies (permissive, OR-ed with the existing ones; SELECT only).
--    Conversations and messages get NO new policy: the Hub's own policies of 000095 already let a live grant read them, with the CONTRACT's
--    queue scope (service_scope.queue_ids) applied. A second policy for the same rows would either repeat that or, worse, forget the queue scope.
--    The key `conversation.read` is enforced by the route (Delegable) and will become the single definition when the grant-based predicate is
--    made key-aware (ADR-0040 phase 04). Contacts and media are NEW for the Hub, so each requires its own domain AND inherits the visibility
--    of the conversation it belongs to (the EXISTS runs under the caller's RLS): a contract limited to some queues never reaches the contacts
--    or the files of the others.
CREATE POLICY contacts_read_delegated ON contacts FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'read'))
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.contact_id = contacts.id AND c.tenant_id = contacts.tenant_id));
CREATE POLICY message_media_read_delegated ON message_media FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('media', 'read'))
         AND EXISTS (SELECT 1 FROM messages m WHERE m.id = message_media.message_id AND m.tenant_id = message_media.tenant_id));
CREATE POLICY message_outbound_media_read_delegated ON message_outbound_media FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('media', 'read'))
         AND EXISTS (SELECT 1 FROM messages m WHERE m.id = message_outbound_media.message_id AND m.tenant_id = message_outbound_media.tenant_id));
CREATE POLICY message_media_analysis_read_delegated ON message_media_analysis FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('media', 'read'))
         AND EXISTS (SELECT 1 FROM messages m WHERE m.id = message_media_analysis.message_id AND m.tenant_id = message_media_analysis.tenant_id));
