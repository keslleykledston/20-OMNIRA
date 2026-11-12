-- Rollback only safe if no row currently uses 'partner'/'supplier' (would otherwise violate the restored constraint).
ALTER TABLE wa_groups DROP CONSTRAINT wa_groups_purpose_check;
ALTER TABLE wa_groups ADD CONSTRAINT wa_groups_purpose_check
  CHECK (purpose IN ('client_management','client_support','internal_management','internal_team','other'));
