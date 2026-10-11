-- ADR-0042 (slices 2-3): a database session context that arbitrary SQL cannot forge.
--
-- Until now every RLS decision trusted four settings the application role can write with set_config(): app.current_user_id, app.is_system_admin,
-- app.acting_hub and app.acting_tenant. Anyone able to run arbitrary SQL in an application session could turn the administrator flag on or clear the
-- acting hub. From here the trusted values live in a table only the owner can write (session_ctx_verified, one row per backend, valid for the transaction that
-- wrote it) and are put there by owner-rights functions:
--   ctx_challenge()       the application asks for a challenge as the first statement of a transaction: a fresh random nonce, kept in the registry row of THIS transaction
--   ctx_begin(raw)        the application answers with a context it SIGNED, with a key from its environment, over that nonce
--                         (b1|<user uuid>|<0|1 system>|<nonce>|<hmac-sha256 hex>); the HMAC is checked ONCE per transaction. The token is good for exactly the transaction that
--                         issued the nonce, once: a copy captured anywhere else (another session, an earlier transaction of the same backend, a rolled-back request) is useless,
--                         and nothing depends on clocks. In enforce a refused context is an ERROR (the request does not run); in off/measure it is a `false`.
--   lock_served_tenant    records the acting hub and tenant (it already proves the delegation); nothing to sign, it runs inside the database
-- The HMAC key lives in a table the application role cannot read, so arbitrary SQL cannot produce a context that ctx_begin accepts, and cannot write the table.
-- Reading the verified context in a policy is one primary-key lookup (the first design verified the HMAC on every call: 36 us per call against ~3 us, measured).
--
-- Three modes (table session_ctx_mode, changed only by the owner through ctx_set_mode):
--   off      the legacy settings decide (what ran until now); the verified context is ignored
--   measure  the legacy settings still decide, but every divergence from the verified value raises a WARNING (to find any code path that does not sign)
--   enforce  ONLY the verified context counts; a missing, forged or wrongly-signed one is "no context" (no access); the legacy settings are ignored
-- This migration leaves the mode at 'off': nothing changes until an operator installs a key and moves the mode.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ---------------------------------------------------------------------------------------------------------------------------------------------------
-- 1. Storage the application role cannot touch
-- ---------------------------------------------------------------------------------------------------------------------------------------------------
CREATE TABLE session_ctx_keys (
  state      TEXT PRIMARY KEY CHECK (state IN ('current', 'previous')),
  key        BYTEA NOT NULL CHECK (octet_length(key) >= 32),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE session_ctx_mode (
  singleton  BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
  mode       TEXT NOT NULL DEFAULT 'off' CHECK (mode IN ('off', 'measure', 'enforce')),
  changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO session_ctx_mode (singleton, mode) VALUES (true, 'off');

-- The default privileges of this schema grant every new table to omnira_app: take them back, and keep RLS on with no policy as a second wall.
REVOKE ALL ON session_ctx_keys, session_ctx_mode FROM PUBLIC, omnira_app;
ALTER TABLE session_ctx_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE session_ctx_mode ENABLE ROW LEVEL SECURITY;

-- ---------------------------------------------------------------------------------------------------------------------------------------------------
-- 2. Owner-only administration (never callable by the application role)
-- ---------------------------------------------------------------------------------------------------------------------------------------------------
-- Installs the signing key. The current key becomes the previous one (still accepted, so instances that have not restarted keep working), the one before is dropped.
CREATE FUNCTION ctx_install_key(p_key BYTEA) RETURNS TEXT AS $$
BEGIN
  IF p_key IS NULL OR octet_length(p_key) < 32 THEN
    RAISE EXCEPTION 'the session context key must have at least 32 bytes';
  END IF;
  IF EXISTS (SELECT 1 FROM public.session_ctx_keys WHERE state = 'current' AND key = p_key) THEN
    RETURN 'unchanged';
  END IF;
  DELETE FROM public.session_ctx_keys WHERE state = 'previous';
  UPDATE public.session_ctx_keys SET state = 'previous' WHERE state = 'current';
  INSERT INTO public.session_ctx_keys (state, key) VALUES ('current', p_key);
  RETURN 'installed';
END;
$$ LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

CREATE FUNCTION ctx_drop_previous_key() RETURNS INTEGER AS $$
DECLARE n INTEGER;
BEGIN
  DELETE FROM public.session_ctx_keys WHERE state = 'previous';
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n;
END;
$$ LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

REVOKE ALL ON FUNCTION ctx_install_key(BYTEA), ctx_drop_previous_key() FROM PUBLIC, omnira_app;

-- ---------------------------------------------------------------------------------------------------------------------------------------------------
-- 3. The verified context of the current transaction (written only by the functions below)
-- ---------------------------------------------------------------------------------------------------------------------------------------------------
-- One row per backend; a row counts only for the transaction that wrote it (xid), so nothing carries over to the next transaction of a pooled connection.
-- UNLOGGED: it never needs to survive a crash (it only describes the transaction in flight).
CREATE UNLOGGED TABLE session_ctx_verified (
  pid            INTEGER PRIMARY KEY,
  xid            xid8 NOT NULL,                    -- the TOP-LEVEL transaction the row belongs to (savepoints share it); a row of an earlier transaction never matches
  nonce          TEXT,                             -- the challenge issued to this transaction, until ctx_begin consumes it
  base_set       BOOLEAN NOT NULL DEFAULT false,   -- ctx_begin already accepted a context in this transaction (a second one is refused)
  uid            UUID,
  sys            BOOLEAN NOT NULL DEFAULT false,
  acting_hub     UUID,
  acting_tenant  UUID
);
REVOKE ALL ON session_ctx_verified FROM PUBLIC, omnira_app;
ALTER TABLE session_ctx_verified ENABLE ROW LEVEL SECURITY;

-- Splits "<tag>|f1|..|fN|<sig>" and returns the pieces when the tag, the number of fields and the signature are right (either installed key); otherwise NULL.
-- The signatures are compared through their SHA-256, so the comparison time does not depend on how many leading bytes of a forged signature are right.
CREATE FUNCTION ctx_signed(p_raw TEXT, p_tag TEXT, p_fields INTEGER) RETURNS TEXT[] AS $$
DECLARE
  parts   TEXT[];
  sig     TEXT;
  payload TEXT;
BEGIN
  IF p_raw IS NULL OR p_raw = '' THEN RETURN NULL; END IF;
  parts := string_to_array(p_raw, '|');
  IF cardinality(parts) <> p_fields + 2 OR parts[1] <> p_tag THEN RETURN NULL; END IF;
  sig := parts[cardinality(parts)];
  IF sig !~ '^[0-9a-f]{64}$' THEN RETURN NULL; END IF;
  payload := array_to_string(parts[1:cardinality(parts) - 1], '|');
  IF NOT EXISTS (
    SELECT 1 FROM public.session_ctx_keys k
    WHERE sha256(decode(sig, 'hex')) = sha256(public.hmac(convert_to(payload, 'UTF8'), k.key, 'sha256'))
  ) THEN
    RETURN NULL;
  END IF;
  RETURN parts;
END;
$$ LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

-- What a refused context means: in enforce, an error (the application must not run a transaction that has no identity, and a misconfigured key must be loud);
-- in off and measure, false (the legacy settings still decide, so the rollout cannot break requests).
CREATE FUNCTION ctx_refused() RETURNS BOOLEAN AS $$
BEGIN
  IF COALESCE((SELECT mode FROM public.session_ctx_mode), 'enforce') = 'enforce' THEN
    RAISE EXCEPTION 'session_ctx: the session context was refused' USING ERRCODE = '28000';
  END IF;
  RETURN false;
END;
$$ LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

-- The first statement of every transaction of the application: a fresh random nonce, remembered in the registry row of THIS transaction. Anyone can ask for one (and learn
-- nothing: only the holder of the key can sign it). Asking again replaces the nonce but never touches a context already accepted in this transaction.
CREATE FUNCTION ctx_challenge() RETURNS TEXT AS $$
DECLARE
  n TEXT := replace(gen_random_uuid()::TEXT, '-', '');
BEGIN
  INSERT INTO public.session_ctx_verified AS v (pid, xid, nonce) VALUES (pg_backend_pid(), pg_current_xact_id(), n)
  ON CONFLICT (pid) DO UPDATE SET
    uid           = CASE WHEN v.xid = pg_current_xact_id() THEN v.uid END,
    sys           = CASE WHEN v.xid = pg_current_xact_id() THEN v.sys ELSE false END,
    base_set      = CASE WHEN v.xid = pg_current_xact_id() THEN v.base_set ELSE false END,
    acting_hub    = CASE WHEN v.xid = pg_current_xact_id() THEN v.acting_hub END,
    acting_tenant = CASE WHEN v.xid = pg_current_xact_id() THEN v.acting_tenant END,
    xid           = pg_current_xact_id(),
    nonce         = n;
  RETURN n;
END;
$$ LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

-- Called by the application right after ctx_challenge, with a context signed with an installed key over THAT nonce. Accepted only if the nonce is the one issued to this very transaction
-- (so a token is good for one transaction of one backend, once: a captured copy is useless anywhere else, whatever happens to the request that carried it) and no context was accepted in
-- this transaction yet (a second one could only be somebody trying to change who the session is). Records the identity for this transaction.
CREATE FUNCTION ctx_begin(p_raw TEXT) RETURNS BOOLEAN AS $$
DECLARE
  p TEXT[] := public.ctx_signed(p_raw, 'b1', 3);   -- [tag, user, system, nonce, sig]
BEGIN
  IF p IS NULL THEN RETURN public.ctx_refused(); END IF;
  IF p[3] NOT IN ('0', '1') OR p[4] !~ '^[0-9a-f]{32}$'
     OR p[2] !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
    RETURN public.ctx_refused();
  END IF;
  UPDATE public.session_ctx_verified v
     SET base_set = true, nonce = NULL, uid = p[2]::UUID, sys = (p[3] = '1')
   WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned() AND NOT v.base_set AND v.nonce = p[4];
  IF NOT FOUND THEN RETURN public.ctx_refused(); END IF;
  RETURN true;
END;
$$ LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

-- Records the acting hub and tenant of this transaction (the caller, lock_served_tenant, has already proven the delegation). Keeps a base context recorded in the same transaction.
CREATE FUNCTION ctx_set_acting(p_hub_id UUID, p_tenant_id UUID) RETURNS VOID AS $$
  INSERT INTO public.session_ctx_verified AS v (pid, xid, acting_hub, acting_tenant)
  VALUES (pg_backend_pid(), pg_current_xact_id(), p_hub_id, p_tenant_id)
  ON CONFLICT (pid) DO UPDATE SET
    uid           = CASE WHEN v.xid = pg_current_xact_id() THEN v.uid END,
    sys           = CASE WHEN v.xid = pg_current_xact_id() THEN v.sys ELSE false END,
    base_set      = CASE WHEN v.xid = pg_current_xact_id() THEN v.base_set ELSE false END,
    nonce         = CASE WHEN v.xid = pg_current_xact_id() THEN v.nonce END,
    xid           = pg_current_xact_id(),
    acting_hub    = EXCLUDED.acting_hub,
    acting_tenant = EXCLUDED.acting_tenant;
$$ LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

REVOKE ALL ON FUNCTION ctx_signed(TEXT, TEXT, INTEGER), ctx_set_acting(UUID, UUID), ctx_refused() FROM PUBLIC, omnira_app;
-- ctx_begin stays callable by the application role: calling it with anything but a context signed with the key changes nothing.

-- ---------------------------------------------------------------------------------------------------------------------------------------------------
-- 4. The readers every policy uses (their definition follows the mode: ctx_install_readers rewrites them when the mode changes)
-- ---------------------------------------------------------------------------------------------------------------------------------------------------
-- off      = exactly what ran before this migration (plain SQL over the legacy settings: inlined by the planner, zero cost)
-- measure  = the legacy value decides, a difference from the verified one is reported (WARNING)
-- enforce  = one primary-key lookup of the verified row of THIS transaction (the row of an earlier transaction of the same backend does not count)
CREATE FUNCTION ctx_measure(p_what TEXT, p_legacy TEXT, p_verified TEXT) RETURNS VOID AS $$
BEGIN
  IF p_legacy IS DISTINCT FROM p_verified THEN
    RAISE WARNING 'session_ctx divergence: % legacy=% verified=%', p_what, p_legacy, p_verified;
  END IF;
END;
$$ LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
CREATE FUNCTION ctx_verified(p_col TEXT) RETURNS TEXT AS $$
  SELECT CASE p_col WHEN 'uid' THEN v.uid::TEXT WHEN 'sys' THEN v.sys::TEXT WHEN 'hub' THEN v.acting_hub::TEXT WHEN 'tenant' THEN v.acting_tenant::TEXT END
  FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned();
$$ LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE ALL ON FUNCTION ctx_measure(TEXT, TEXT, TEXT), ctx_verified(TEXT) FROM PUBLIC, omnira_app;

CREATE FUNCTION ctx_install_readers(p_mode TEXT) RETURNS VOID AS $fn$
DECLARE
  uuid_re CONSTANT TEXT := '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';
  sdef    CONSTANT TEXT := ' SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp';
BEGIN
  IF p_mode NOT IN ('off', 'measure', 'enforce') THEN RAISE EXCEPTION 'unknown mode %', p_mode; END IF;

  IF p_mode = 'off' THEN
    -- the definitions of migrations 000027, 000109, 000111 (plain, invoker rights, inlinable)
    EXECUTE $q$CREATE OR REPLACE FUNCTION public.current_user_id() RETURNS UUID AS $b$
      SELECT NULLIF(current_setting('app.current_user_id', TRUE), '')::UUID;
    $b$ LANGUAGE sql STABLE$q$;
    EXECUTE $q$CREATE OR REPLACE FUNCTION public.is_system_admin() RETURNS BOOLEAN AS $b$
      SELECT COALESCE(NULLIF(current_setting('app.is_system_admin', TRUE), '')::BOOLEAN, FALSE);
    $b$ LANGUAGE sql STABLE$q$;
    EXECUTE format($q$CREATE OR REPLACE FUNCTION public.acting_hub() RETURNS UUID AS $b$
      SELECT CASE WHEN v ~* %L THEN v::UUID END FROM (SELECT NULLIF(current_setting('app.acting_hub', true), '') AS v) s;
    $b$ LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp$q$, uuid_re);
    EXECUTE format($q$CREATE OR REPLACE FUNCTION public.acting_tenant() RETURNS UUID AS $b$
      SELECT CASE WHEN v ~* %L THEN v::UUID END FROM (SELECT NULLIF(current_setting('app.acting_tenant', true), '') AS v) s;
    $b$ LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp$q$, uuid_re);
    EXECUTE $q$CREATE OR REPLACE FUNCTION public.ctx_acting_present() RETURNS BOOLEAN AS $b$
      SELECT NULLIF(current_setting('app.acting_hub', true), '') IS NOT NULL;
    $b$ LANGUAGE sql STABLE$q$;

  ELSIF p_mode = 'measure' THEN
    EXECUTE format($q$CREATE OR REPLACE FUNCTION public.current_user_id() RETURNS UUID AS $b$
      DECLARE legacy UUID := NULLIF(current_setting('app.current_user_id', TRUE), '')::UUID;
      BEGIN PERFORM public.ctx_measure('current_user_id', legacy::TEXT, public.ctx_verified('uid')); RETURN legacy; END;
    $b$ LANGUAGE plpgsql STABLE%s$q$, sdef);
    EXECUTE format($q$CREATE OR REPLACE FUNCTION public.is_system_admin() RETURNS BOOLEAN AS $b$
      DECLARE legacy BOOLEAN := COALESCE(NULLIF(current_setting('app.is_system_admin', TRUE), '')::BOOLEAN, FALSE);
      BEGIN PERFORM public.ctx_measure('is_system_admin', legacy::TEXT, COALESCE(public.ctx_verified('sys'), 'false')); RETURN legacy; END;
    $b$ LANGUAGE plpgsql STABLE%s$q$, sdef);
    EXECUTE format($q$CREATE OR REPLACE FUNCTION public.acting_hub() RETURNS UUID AS $b$
      DECLARE legacy UUID;
      BEGIN
        SELECT CASE WHEN v ~* %L THEN v::UUID END INTO legacy FROM (SELECT NULLIF(current_setting('app.acting_hub', true), '') AS v) s;
        PERFORM public.ctx_measure('acting_hub', legacy::TEXT, public.ctx_verified('hub'));
        RETURN legacy;
      END;
    $b$ LANGUAGE plpgsql STABLE%s$q$, uuid_re, sdef);
    EXECUTE format($q$CREATE OR REPLACE FUNCTION public.acting_tenant() RETURNS UUID AS $b$
      DECLARE legacy UUID;
      BEGIN
        SELECT CASE WHEN v ~* %L THEN v::UUID END INTO legacy FROM (SELECT NULLIF(current_setting('app.acting_tenant', true), '') AS v) s;
        PERFORM public.ctx_measure('acting_tenant', legacy::TEXT, public.ctx_verified('tenant'));
        RETURN legacy;
      END;
    $b$ LANGUAGE plpgsql STABLE%s$q$, uuid_re, sdef);
    EXECUTE format($q$CREATE OR REPLACE FUNCTION public.ctx_acting_present() RETURNS BOOLEAN AS $b$
      DECLARE legacy BOOLEAN := NULLIF(current_setting('app.acting_hub', true), '') IS NOT NULL;
      BEGIN PERFORM public.ctx_measure('acting_present', legacy::TEXT, (public.ctx_verified('hub') IS NOT NULL)::TEXT); RETURN legacy; END;
    $b$ LANGUAGE plpgsql STABLE%s$q$, sdef);

  ELSE
    EXECUTE format($q$CREATE OR REPLACE FUNCTION public.current_user_id() RETURNS UUID AS $b$
      SELECT v.uid FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned();
    $b$ LANGUAGE sql STABLE%s$q$, sdef);
    EXECUTE format($q$CREATE OR REPLACE FUNCTION public.is_system_admin() RETURNS BOOLEAN AS $b$
      SELECT COALESCE((SELECT v.sys FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned()), false);
    $b$ LANGUAGE sql STABLE%s$q$, sdef);
    EXECUTE format($q$CREATE OR REPLACE FUNCTION public.acting_hub() RETURNS UUID AS $b$
      SELECT v.acting_hub FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned();
    $b$ LANGUAGE sql STABLE%s$q$, sdef);
    EXECUTE format($q$CREATE OR REPLACE FUNCTION public.acting_tenant() RETURNS UUID AS $b$
      SELECT v.acting_tenant FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned();
    $b$ LANGUAGE sql STABLE%s$q$, sdef);
    EXECUTE format($q$CREATE OR REPLACE FUNCTION public.ctx_acting_present() RETURNS BOOLEAN AS $b$
      SELECT EXISTS (SELECT 1 FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned() AND v.acting_hub IS NOT NULL);
    $b$ LANGUAGE sql STABLE%s$q$, sdef);
  END IF;

  -- The three predicates that read the session identity several times per call. off = the definitions of migrations 000097/000109 (raw setting); measure = through the
  -- readers (so a divergence is reported); enforce = ONE read of the verified registry per call (the readers would each look it up: five lookups per row measured at 36 us, one at ~10 us).
  IF p_mode = 'off' THEN
    EXECUTE $q$CREATE OR REPLACE FUNCTION public.has_active_membership(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $b$
      SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
         AND NULLIF(current_setting('app.acting_hub', true), '') IS NULL
         AND EXISTS (SELECT 1 FROM public.memberships WHERE tenant_id = p_tenant_id AND user_id = p_user_id AND status = 'active');
    $b$ LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp$q$;
    EXECUTE $q$CREATE OR REPLACE FUNCTION public.has_active_admin_membership(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $b$
      SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
         AND NULLIF(current_setting('app.acting_hub', true), '') IS NULL
         AND EXISTS (SELECT 1 FROM public.memberships m JOIN public.roles r ON m.role_id = r.id
                     WHERE m.tenant_id = p_tenant_id AND m.user_id = p_user_id AND m.status = 'active' AND r.key = 'tenant_admin');
    $b$ LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp$q$;
    EXECUTE $q$CREATE OR REPLACE FUNCTION public.actor_has_permission(p_tenant_id UUID, p_user_id UUID, p_permission TEXT) RETURNS BOOLEAN AS $b$
      SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
         AND CASE
               WHEN NULLIF(current_setting('app.acting_hub', true), '') IS NULL THEN
                 EXISTS (SELECT 1 FROM public.memberships m JOIN public.role_permissions rp ON rp.role_id = m.role_id
                         WHERE m.tenant_id = p_tenant_id AND m.user_id = p_user_id AND m.status = 'active' AND rp.permission_key = p_permission)
               ELSE COALESCE(p_permission = ANY (public.delegated_permissions(p_tenant_id, p_user_id, public.acting_hub())), false)
             END;
    $b$ LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp$q$;

  ELSIF p_mode = 'measure' THEN
    EXECUTE $q$CREATE OR REPLACE FUNCTION public.has_active_membership(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $b$
      SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
         AND NOT public.ctx_acting_present()
         AND EXISTS (SELECT 1 FROM public.memberships WHERE tenant_id = p_tenant_id AND user_id = p_user_id AND status = 'active');
    $b$ LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp$q$;
    EXECUTE $q$CREATE OR REPLACE FUNCTION public.has_active_admin_membership(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $b$
      SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
         AND NOT public.ctx_acting_present()
         AND EXISTS (SELECT 1 FROM public.memberships m JOIN public.roles r ON m.role_id = r.id
                     WHERE m.tenant_id = p_tenant_id AND m.user_id = p_user_id AND m.status = 'active' AND r.key = 'tenant_admin');
    $b$ LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp$q$;
    EXECUTE $q$CREATE OR REPLACE FUNCTION public.actor_has_permission(p_tenant_id UUID, p_user_id UUID, p_permission TEXT) RETURNS BOOLEAN AS $b$
      SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
         AND CASE
               WHEN NOT public.ctx_acting_present() THEN
                 EXISTS (SELECT 1 FROM public.memberships m JOIN public.role_permissions rp ON rp.role_id = m.role_id
                         WHERE m.tenant_id = p_tenant_id AND m.user_id = p_user_id AND m.status = 'active' AND rp.permission_key = p_permission)
               ELSE COALESCE(p_permission = ANY (public.delegated_permissions(p_tenant_id, p_user_id, public.acting_hub())), false)
             END;
    $b$ LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp$q$;

  ELSE
    -- c = the verified row of THIS transaction (all NULL when there is none): same three-valued logic as current_user_id() / is_system_admin() / ctx_acting_present() in this mode
    EXECUTE $q$CREATE OR REPLACE FUNCTION public.has_active_membership(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $b$
      SELECT (p_user_id = c.uid OR COALESCE(c.sys, false))
         AND NOT (c.acting_hub IS NOT NULL)
         AND EXISTS (SELECT 1 FROM public.memberships WHERE tenant_id = p_tenant_id AND user_id = p_user_id AND status = 'active')
      FROM (SELECT 1) one LEFT JOIN public.session_ctx_verified c ON c.pid = pg_backend_pid() AND c.xid = pg_current_xact_id_if_assigned();
    $b$ LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp$q$;
    EXECUTE $q$CREATE OR REPLACE FUNCTION public.has_active_admin_membership(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $b$
      SELECT (p_user_id = c.uid OR COALESCE(c.sys, false))
         AND NOT (c.acting_hub IS NOT NULL)
         AND EXISTS (SELECT 1 FROM public.memberships m JOIN public.roles r ON m.role_id = r.id
                     WHERE m.tenant_id = p_tenant_id AND m.user_id = p_user_id AND m.status = 'active' AND r.key = 'tenant_admin')
      FROM (SELECT 1) one LEFT JOIN public.session_ctx_verified c ON c.pid = pg_backend_pid() AND c.xid = pg_current_xact_id_if_assigned();
    $b$ LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp$q$;
    EXECUTE $q$CREATE OR REPLACE FUNCTION public.actor_has_permission(p_tenant_id UUID, p_user_id UUID, p_permission TEXT) RETURNS BOOLEAN AS $b$
      SELECT (p_user_id = c.uid OR COALESCE(c.sys, false))
         AND CASE
               WHEN NOT (c.acting_hub IS NOT NULL) THEN
                 EXISTS (SELECT 1 FROM public.memberships m JOIN public.role_permissions rp ON rp.role_id = m.role_id
                         WHERE m.tenant_id = p_tenant_id AND m.user_id = p_user_id AND m.status = 'active' AND rp.permission_key = p_permission)
               ELSE COALESCE(p_permission = ANY (public.delegated_permissions(p_tenant_id, p_user_id, c.acting_hub)), false)
             END
      FROM (SELECT 1) one LEFT JOIN public.session_ctx_verified c ON c.pid = pg_backend_pid() AND c.xid = pg_current_xact_id_if_assigned();
    $b$ LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp$q$;
  END IF;
END;
$fn$ LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE ALL ON FUNCTION ctx_install_readers(TEXT) FROM PUBLIC, omnira_app;

-- The first definition of ctx_acting_present: it is called by the policies (with the rights of the user running the query), so it must stay callable.
CREATE FUNCTION ctx_acting_present() RETURNS BOOLEAN AS $$ SELECT false $$ LANGUAGE sql STABLE;
DO $$ BEGIN PERFORM public.ctx_install_readers('off'); END $$;

-- 'enforce' needs a key (without one nothing could ever be validated and every session would lose access).
-- The mode and the readers change together (one transaction), only through here.
CREATE FUNCTION ctx_set_mode(p_mode TEXT) RETURNS TEXT AS $$
BEGIN
  IF p_mode NOT IN ('off', 'measure', 'enforce') THEN
    RAISE EXCEPTION 'mode must be off, measure or enforce';
  END IF;
  IF p_mode <> 'off' AND NOT EXISTS (SELECT 1 FROM public.session_ctx_keys WHERE state = 'current') THEN
    RAISE EXCEPTION 'install a session context key first (ctx_install_key)';
  END IF;
  PERFORM public.ctx_install_readers(p_mode);
  UPDATE public.session_ctx_mode SET mode = p_mode, changed_at = now();
  RETURN p_mode;
END;
$$ LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE ALL ON FUNCTION ctx_set_mode(TEXT) FROM PUBLIC, omnira_app;

-- ---------------------------------------------------------------------------------------------------------------------------------------------------
-- 5. The places that read app.acting_hub directly now ask for "is there an acting context" through the same door
-- ---------------------------------------------------------------------------------------------------------------------------------------------------
-- (has_active_membership, has_active_admin_membership and actor_has_permission are defined per mode by ctx_install_readers, above)

-- The six hot read policies take the session's identity as an InitPlan: it is read ONCE per query instead of once per row (the per-row call of has_active_membership remains).
ALTER POLICY conversations_read_tenant  ON conversations  USING ((NOT (SELECT public.ctx_acting_present()) AND has_active_membership(tenant_id, (SELECT current_user_id()))) OR (SELECT is_system_admin()));
ALTER POLICY messages_read_tenant       ON messages       USING ((NOT (SELECT public.ctx_acting_present()) AND has_active_membership(tenant_id, (SELECT current_user_id()))) OR (SELECT is_system_admin()));
ALTER POLICY contacts_read_tenant       ON contacts       USING ((NOT (SELECT public.ctx_acting_present()) AND has_active_membership(tenant_id, (SELECT current_user_id()))) OR (SELECT is_system_admin()));
ALTER POLICY tickets_read_tenant        ON tickets        USING ((NOT (SELECT public.ctx_acting_present()) AND has_active_membership(tenant_id, (SELECT current_user_id()))) OR (SELECT is_system_admin()));
ALTER POLICY message_media_read_tenant  ON message_media  USING ((NOT (SELECT public.ctx_acting_present()) AND has_active_membership(tenant_id, (SELECT current_user_id()))) OR (SELECT is_system_admin()));
ALTER POLICY contact_notes_read_tenant  ON contact_notes  USING ((NOT (SELECT public.ctx_acting_present()) AND has_active_membership(tenant_id, (SELECT current_user_id()))) OR (SELECT is_system_admin()));

-- ---------------------------------------------------------------------------------------------------------------------------------------------------
-- 6. The only writer of the acting context records it in the verified table (everything else in the function is unchanged from migration 114)
-- ---------------------------------------------------------------------------------------------------------------------------------------------------
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
  PERFORM set_config('app.acting_tenant', p_tenant_id::TEXT, true);
  PERFORM public.ctx_set_acting(p_hub_id, p_tenant_id);
  PERFORM set_config('jit', 'off', true);
  RETURN true;
END;
$$ LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
