-- ADR-0059 addendum 10: groups classified or linked to a client BEFORE the auto-enable-on-classify/link
-- behavior existed (migration 000194) never got the retroactive benefit of it — their purpose/account was
-- set under the old code, so enabling never fired. This is a one-time backfill for exactly that gap: a
-- disabled group that has an explicit purpose/account/additional-account set, AND that was never itself
-- enabled before (enabled_by IS NULL). The enabled_by IS NULL guard is what protects a group an admin
-- deliberately turned off after it had been read for a while — that group was enabled at some point, so
-- enabled_by is set, and this backfill leaves it alone.
UPDATE wa_groups g
SET enabled = true,
    enabled_by = COALESCE(
      g.purpose_set_by,
      g.account_set_by,
      (SELECT a.created_by FROM wa_group_additional_accounts a
       WHERE a.tenant_id = g.tenant_id AND a.group_id = g.id ORDER BY a.created_at LIMIT 1)
    ),
    enabled_at = now(),
    updated_at = now()
WHERE g.enabled = false
  AND g.enabled_by IS NULL
  AND (
    g.purpose_set_by IS NOT NULL
    OR g.account_id IS NOT NULL
    OR EXISTS (
      SELECT 1 FROM wa_group_additional_accounts a
      WHERE a.tenant_id = g.tenant_id AND a.group_id = g.id
    )
  );
