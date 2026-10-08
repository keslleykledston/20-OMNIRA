-- ADR-0038: the "platform operator" is the human who may create companies, create Hubs and switch companies and their
-- capabilities on and off. It is NOT a tenant role and NOT the internal `system_admin` session mode.
--
-- Who is an operator is decided by this table, read by the SERVER for the authenticated session user on every request;
-- it is never taken from a token claim or a request field. Rows are written only through a system session
-- (omnira-hubctl platform-operator ...): nobody can promote themselves, and the table is invisible to everyone but
-- the user it names.
CREATE TABLE platform_operators (
  user_id     UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
  granted_by  TEXT NOT NULL CHECK (char_length(granted_by) BETWEEN 1 AND 100),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at  TIMESTAMPTZ,
  CHECK ((status = 'revoked') = (revoked_at IS NOT NULL))
);

ALTER TABLE platform_operators ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform_operators FORCE ROW LEVEL SECURITY;

CREATE POLICY platform_operators_read ON platform_operators FOR SELECT
  USING (is_system_admin() OR user_id = current_user_id());
CREATE POLICY platform_operators_insert ON platform_operators FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY platform_operators_update ON platform_operators FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY platform_operators_delete ON platform_operators FOR DELETE USING (is_system_admin());

GRANT SELECT, INSERT, UPDATE, DELETE ON platform_operators TO omnira_app;

-- Answers only for the session user (or a system session), so it cannot be used to probe who else is an operator.
CREATE OR REPLACE FUNCTION is_platform_operator(p_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND EXISTS (SELECT 1 FROM public.platform_operators WHERE user_id = p_user_id AND status = 'active');
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

REVOKE EXECUTE ON FUNCTION is_platform_operator(UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION is_platform_operator(UUID) TO omnira_app;
