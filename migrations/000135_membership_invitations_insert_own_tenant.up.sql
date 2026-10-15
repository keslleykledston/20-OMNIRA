-- 000035 wrote `m.tenant_id = tenant_id` inside the subquery, where `tenant_id` resolves to m.tenant_id: the check was always
-- true and whoever held membership.manage in ONE tenant passed this policy for an invitation of ANY tenant. The HTTP handler
-- already scopes by the URL tenant; this restores RLS as the second barrier. Production received the same policy by hand on
-- 2026-10-14, so applying it there changes nothing.
DROP POLICY IF EXISTS membership_invitations_insert ON membership_invitations;

CREATE POLICY membership_invitations_insert ON membership_invitations FOR INSERT
  WITH CHECK (
    EXISTS (
      SELECT 1 FROM memberships m
      JOIN role_permissions rp ON rp.role_id = m.role_id
      WHERE m.tenant_id = membership_invitations.tenant_id AND m.user_id = current_user_id()
        AND m.status = 'active' AND rp.permission_key = 'membership.manage'
    )
  );
