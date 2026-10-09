-- ADR-0039: only an admin of the HUB may put a person in more than one instance (company).
--
-- A company's own administrator invites people into THEIR company. If the invited e-mail already belongs to a person who works
-- in another instance (an active membership elsewhere, or membership of a hub), the invitation must be refused and sent to the
-- hub admin instead. The administrator cannot see other companies' rows (RLS), so the question is answered here, with the
-- minimum disclosed: a boolean, and only to someone allowed to manage this company's people (or to the system session that
-- accepts the invitation). Anyone else gets false: the functions are not an oracle on who works where.

-- By user id: used when a membership is (re)activated (PATCH /team) and, through the e-mail variant, for invitations.
CREATE OR REPLACE FUNCTION user_works_in_other_instance(p_tenant_id UUID, p_user_id UUID) RETURNS BOOLEAN AS $$
  SELECT CASE
    WHEN NOT (
      public.is_system_admin()
      OR EXISTS (
        SELECT 1 FROM public.memberships m
        JOIN public.role_permissions rp ON rp.role_id = m.role_id
        WHERE m.tenant_id = p_tenant_id AND m.user_id = public.current_user_id()
          AND m.status = 'active' AND rp.permission_key = 'membership.manage'
      )
    ) THEN false
    ELSE (
      EXISTS (SELECT 1 FROM public.memberships m2 WHERE m2.user_id = p_user_id AND m2.status = 'active' AND m2.tenant_id <> p_tenant_id)
      OR EXISTS (SELECT 1 FROM public.hub_memberships hm WHERE hm.user_id = p_user_id)
    )
  END;
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

-- By e-mail: the invitation flow knows only the address.
CREATE OR REPLACE FUNCTION person_works_in_other_instance(p_tenant_id UUID, p_email TEXT) RETURNS BOOLEAN AS $$
  SELECT CASE
    WHEN NOT (
      public.is_system_admin()
      OR EXISTS (
        SELECT 1 FROM public.memberships m
        JOIN public.role_permissions rp ON rp.role_id = m.role_id
        WHERE m.tenant_id = p_tenant_id AND m.user_id = public.current_user_id()
          AND m.status = 'active' AND rp.permission_key = 'membership.manage'
      )
    ) THEN false
    ELSE EXISTS (
      SELECT 1 FROM public.users u
      WHERE lower(u.email) = lower(p_email)
        AND (
          EXISTS (SELECT 1 FROM public.memberships m2 WHERE m2.user_id = u.id AND m2.status = 'active' AND m2.tenant_id <> p_tenant_id)
          OR EXISTS (SELECT 1 FROM public.hub_memberships hm WHERE hm.user_id = u.id)
        )
    )
  END;
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

REVOKE EXECUTE ON FUNCTION user_works_in_other_instance(UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION user_works_in_other_instance(UUID, UUID) TO omnira_app;
REVOKE EXECUTE ON FUNCTION person_works_in_other_instance(UUID, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION person_works_in_other_instance(UUID, TEXT) TO omnira_app;
