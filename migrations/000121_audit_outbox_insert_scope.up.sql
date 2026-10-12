-- Pre-gate review (Codex, 2026-10-12) of the database layer, assuming an attacker who can run ARBITRARY SQL as the application role:
--
-- H-01  audit_events and outbox_events accepted an INSERT from ANY authenticated person for ANY company and ANY actor. A signed person could write an audit row that
--       says another person did something in another company, or enqueue an outbox event for a company they have nothing to do with.
-- M-01  lock_served_tenant could be called again inside the same transaction and replace the acting Hub/company by another valid grant of the same person.
-- L-02  CREATE on the public schema was not revoked explicitly from the application role (PostgreSQL 15+ defaults make it safe; now it is stated).
--
-- What an INSERT into audit_events / outbox_events must now be, unless the session is a system session (workers, the platform operator, the migrator):
--   * the actor of an audit row is the person of the session (or none);
--   * the company, when there is one, is a company the person belongs to, serves through a live Hub grant, or manages through the Hub.
-- Every legitimate writer is one of those (checked against the 444 audit rows of production and by the whole integration gate and the browser E2Es).

DROP POLICY audit_events_insert_authenticated ON audit_events;
CREATE POLICY audit_events_insert_scoped ON audit_events FOR INSERT
  WITH CHECK (
    is_system_admin()
    OR (current_user_id() IS NOT NULL
        AND (actor_id IS NULL OR actor_id = current_user_id())
        AND (tenant_id IS NULL
             OR has_active_membership(tenant_id, current_user_id())
             OR has_active_hub_access(current_user_id(), tenant_id, NULL, NULL, false)
             OR has_hub_manage_access(tenant_id, current_user_id())))
  );

DROP POLICY outbox_events_insert_authenticated ON outbox_events;
CREATE POLICY outbox_events_insert_scoped ON outbox_events FOR INSERT
  WITH CHECK (
    is_system_admin()
    OR (current_user_id() IS NOT NULL
        AND (has_active_membership(tenant_id, current_user_id())
             OR has_active_hub_access(current_user_id(), tenant_id, NULL, NULL, false)
             OR has_hub_manage_access(tenant_id, current_user_id())))
  );

-- One acting context per transaction: asking again for the SAME Hub and company is idempotent, asking for another pair is refused (the first answer stands).
CREATE OR REPLACE FUNCTION lock_served_tenant(p_tenant_id UUID, p_user_id UUID, p_hub_id UUID) RETURNS BOOLEAN AS $$
BEGIN
  IF NOT (p_user_id = public.current_user_id() OR public.is_system_admin()) THEN
    RETURN false;
  END IF;
  IF public.acting_hub() IS NOT NULL THEN
    RETURN public.acting_hub() = p_hub_id AND public.acting_tenant() = p_tenant_id;
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
  PERFORM set_config('app.acting_tenant', p_tenant_id::TEXT, true);
  PERFORM public.ctx_set_acting(p_hub_id, p_tenant_id);
  PERFORM set_config('jit', 'off', true);
  RETURN true;
END;
$$ LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

REVOKE CREATE ON SCHEMA public FROM PUBLIC, omnira_app;
