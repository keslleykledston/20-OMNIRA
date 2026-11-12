-- ADR-0059 addendum 4: wa_groups.purpose (migration 000176) only distinguished client/internal matters.
-- K3G_Grupos' real book of WhatsApp groups also needs "this group is our partner" and "this group is
-- our supplier" as first-class tags, alongside the client/internal ones, so the same closed-list
-- (administrator-set, audited via purpose_set_by/at) now also accepts 'partner' and 'supplier'. No
-- management/support split for these two: unlike client_*/internal_*, nothing asked for that nuance here.
ALTER TABLE wa_groups DROP CONSTRAINT wa_groups_purpose_check;
ALTER TABLE wa_groups ADD CONSTRAINT wa_groups_purpose_check
  CHECK (purpose IN ('client_management','client_support','internal_management','internal_team','partner','supplier','other'));
