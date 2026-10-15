-- ADR-0049 addendum 2: a protocol is issued ONLY by the database function below. The application role can no longer create a counter row at an arbitrary prefix or
-- number, move a counter, or choose the first number with raw SQL (Codex rounds 8-9, risk R-5), and opening a session no longer depends on READING the counter
-- row (risk R-6: INSERT ... RETURNING was subject to the read policy).
--
--  * service_issue_protocol(tenant, kind) runs as the table owner. It asks the caller's key (service.create, or the system; in a Hub delegation only the acting
--    company) and then takes the next number. The first number (1001 for sessions, 1000 for cases) and the case prefix (OC) live HERE; for a session, a company
--    that has no row derives its prefix from its name (service_prefix_candidates) and takes the first one no other company uses.
--  * The application role may still RESERVE a prefix (a row with last_value = 0), and only with tenant.manage: that is the administrator's choice (000155).
--  * The guard trigger refuses, for the application role, any change of a counter value and any new row that is not a reservation.
SET LOCAL lock_timeout = '5s';

-- the case counter has one prefix
ALTER TABLE service_counters ADD CONSTRAINT service_counters_case_prefix_chk CHECK (kind <> 'case' OR prefix = 'OC');

-- Candidates for a company name, best first: the first 3 letters ("Veloso" -> VEL), then 4, 5, 6, then the first 2 letters + any letter, then the first letter +
-- any two. Accents are dropped by decomposition; digits and punctuation are ignored; fewer than two letters falls back to "AT".
CREATE FUNCTION service_prefix_candidates(p_name TEXT) RETURNS SETOF TEXT AS $$
DECLARE base TEXT; n INT; i INT; j INT;
BEGIN
  base := regexp_replace(normalize(upper(COALESCE(p_name, '')), NFD), '[^A-Z]', '', 'g');
  IF length(base) < 2 THEN base := 'AT'; END IF;
  IF length(base) >= 3 THEN RETURN NEXT left(base, 3); ELSE RETURN NEXT base || 'X'; END IF;
  FOR n IN 4..6 LOOP
    IF length(base) >= n THEN RETURN NEXT left(base, n); END IF;
  END LOOP;
  FOR i IN 65..90 LOOP RETURN NEXT left(base, 2) || chr(i); END LOOP;
  FOR i IN 65..90 LOOP FOR j IN 65..90 LOOP RETURN NEXT left(base, 1) || chr(i) || chr(j); END LOOP; END LOOP;
END;
$$ LANGUAGE plpgsql IMMUTABLE SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION service_prefix_candidates(TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION service_prefix_candidates(TEXT) TO omnira_app;

-- Who may ask: the system, or a person (or a Hub delegate acting for exactly this company) who holds service.create.
CREATE FUNCTION service_issue_protocol(p_tenant UUID, p_kind TEXT) RETURNS TABLE (o_prefix TEXT, o_last BIGINT) AS $$
DECLARE v_start BIGINT; v_name TEXT; v_cand TEXT; v_tries INT := 0; v_prefix TEXT; v_last BIGINT; v_con TEXT;
BEGIN
  IF p_kind NOT IN ('session', 'case') THEN
    RAISE EXCEPTION 'unknown counter kind' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF NOT (public.is_system_admin()
          OR (public.actor_has_permission(p_tenant, public.current_user_id(), 'service.create')
              AND (public.acting_hub() IS NULL OR p_tenant = public.acting_tenant()))) THEN
    RAISE EXCEPTION 'issuing a protocol needs service.create' USING ERRCODE = 'insufficient_privilege';
  END IF;
  v_start := CASE p_kind WHEN 'session' THEN 1001 ELSE 1000 END;
  IF p_kind = 'case' THEN
    INSERT INTO public.service_counters AS c (tenant_id, kind, prefix, last_value) VALUES (p_tenant, 'case', 'OC', v_start)
      ON CONFLICT (tenant_id, kind) DO UPDATE SET last_value = CASE WHEN c.last_value = 0 THEN v_start ELSE c.last_value + 1 END, updated_at = now()
      RETURNING c.prefix, c.last_value INTO v_prefix, v_last;
    RETURN QUERY SELECT v_prefix, v_last;
    RETURN;
  END IF;
  SELECT COALESCE(NULLIF(btrim(t.trade_name), ''), t.legal_name) INTO v_name FROM public.tenants t WHERE t.id = p_tenant;
  -- a company that already has its row (chosen, or derived earlier) conflicts on the first candidate and simply moves its counter: the stored prefix is returned
  FOR v_cand IN SELECT x FROM public.service_prefix_candidates(v_name) AS x LOOP
    v_tries := v_tries + 1;
    EXIT WHEN v_tries > 80;
    BEGIN
      INSERT INTO public.service_counters AS c (tenant_id, kind, prefix, last_value) VALUES (p_tenant, 'session', v_cand, v_start)
        ON CONFLICT (tenant_id, kind) DO UPDATE SET last_value = CASE WHEN c.last_value = 0 THEN v_start ELSE c.last_value + 1 END, updated_at = now()
        RETURNING c.prefix, c.last_value INTO v_prefix, v_last;
      RETURN QUERY SELECT v_prefix, v_last;
      RETURN;
    EXCEPTION WHEN unique_violation THEN
      GET STACKED DIAGNOSTICS v_con = CONSTRAINT_NAME;
      IF v_con IS DISTINCT FROM 'service_counters_session_prefix_uq' THEN RAISE; END IF; -- only a taken prefix moves on to the next candidate
    END;
  END LOOP;
  RAISE EXCEPTION 'every candidate protocol prefix is taken' USING ERRCODE = 'unique_violation', CONSTRAINT = 'service_counters_session_prefix_uq';
END;
$$ LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION service_issue_protocol(UUID, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION service_issue_protocol(UUID, TEXT) TO omnira_app;

-- The first prefix nobody uses, for the administrator's screen. Members of the company and the system only; it says nothing about WHO uses the others.
CREATE FUNCTION service_suggest_prefix(p_tenant UUID) RETURNS TEXT AS $$
  SELECT x FROM public.service_prefix_candidates(
           (SELECT COALESCE(NULLIF(btrim(t.trade_name), ''), t.legal_name) FROM public.tenants t WHERE t.id = p_tenant)) AS x
  WHERE (public.is_system_admin() OR public.has_active_membership(p_tenant, public.current_user_id()))
    AND NOT EXISTS (SELECT 1 FROM public.service_counters c WHERE c.kind = 'session' AND c.prefix = x AND c.tenant_id <> p_tenant)
  LIMIT 1;
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION service_suggest_prefix(UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION service_suggest_prefix(UUID) TO omnira_app;

-- Guard for the application role. A trusted writer is (a) a session whose LOGIN role is the owner (superuser or BYPASSRLS: migrations, maintenance) or (b) code that
-- runs inside service_issue_protocol itself (a SECURITY DEFINER function, found on the call stack): NOT any definer function, so a future function that happens to
-- touch the table does not gain the right to move a counter. The application role itself may only RESERVE a prefix and change a reservation.
DROP TRIGGER service_counters_before_update_trg ON service_counters;
DROP FUNCTION service_counters_before_update();
CREATE FUNCTION service_counters_guard() RETURNS trigger AS $$
DECLARE trusted BOOLEAN; stack TEXT;
BEGIN
  trusted := COALESCE((SELECT r.rolsuper OR r.rolbypassrls FROM pg_catalog.pg_roles r WHERE r.rolname = session_user), false);
  IF NOT trusted AND current_user <> session_user THEN
    GET DIAGNOSTICS stack = PG_CONTEXT;
    trusted := position('PL/pgSQL function service_issue_protocol(uuid,text)' IN stack) > 0;
  END IF;
  IF TG_OP = 'INSERT' THEN
    IF NOT trusted AND NEW.last_value <> 0 THEN
      RAISE EXCEPTION 'a protocol counter is created only by service_issue_protocol' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.prefix IS DISTINCT FROM OLD.prefix THEN
    IF OLD.last_value > 0 THEN
      RAISE EXCEPTION 'the protocol prefix is frozen once a protocol was issued' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT (public.is_system_admin() OR public.actor_has_permission(OLD.tenant_id, public.current_user_id(), 'tenant.manage')) THEN
      RAISE EXCEPTION 'changing the protocol prefix needs tenant.manage' USING ERRCODE = 'insufficient_privilege';
    END IF;
  END IF;
  IF NEW.last_value IS DISTINCT FROM OLD.last_value AND NOT trusted THEN
    RAISE EXCEPTION 'a protocol counter moves only through service_issue_protocol' USING ERRCODE = 'insufficient_privilege';
  END IF;
  -- even the trusted writer moves a counter forward by one (a reserved prefix, 0, takes its first number)
  IF NEW.kind IS DISTINCT FROM OLD.kind OR NOT (NEW.last_value = OLD.last_value OR NEW.last_value = OLD.last_value + 1 OR (OLD.last_value = 0 AND NEW.last_value > 0)) THEN
    RAISE EXCEPTION 'a protocol counter moves forward by one' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;
CREATE TRIGGER service_counters_guard_trg BEFORE INSERT OR UPDATE ON service_counters FOR EACH ROW EXECUTE FUNCTION service_counters_guard();

-- Policies of the application role: the administrator reserves/changes a reservation; nobody else writes the table. Reading stays with service.read / tenant.manage.
DROP POLICY service_counters_insert_tenant ON service_counters;
CREATE POLICY service_counters_insert_tenant ON service_counters FOR INSERT
  WITH CHECK (is_system_admin() OR (last_value = 0 AND actor_has_permission(tenant_id, current_user_id(), 'tenant.manage')));
DROP POLICY service_counters_update_tenant ON service_counters;
CREATE POLICY service_counters_update_tenant ON service_counters FOR UPDATE
  USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'tenant.manage'))
  WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'tenant.manage'));
