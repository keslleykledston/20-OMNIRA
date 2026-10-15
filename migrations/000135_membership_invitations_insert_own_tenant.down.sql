-- Back to 000035's text (its tenant comparison is always true; kept only so the migration is reversible).
DROP POLICY IF EXISTS membership_invitations_insert ON membership_invitations;

CREATE POLICY membership_invitations_insert ON membership_invitations FOR INSERT
  WITH CHECK (
    EXISTS (
      SELECT 1 FROM memberships m
      JOIN role_permissions rp ON rp.role_id = m.role_id
      WHERE m.tenant_id = tenant_id AND m.user_id = current_user_id()
        AND m.status = 'active' AND rp.permission_key = 'membership.manage'
    )
  );
