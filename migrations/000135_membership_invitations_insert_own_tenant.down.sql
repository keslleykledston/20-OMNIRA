-- Irreversible on purpose (Codex review, 2026-10-14): going back to 000035's text would reopen the hole this migration closed
-- (its tenant comparison is always true, so an administrator of A could insert invitations into B). The corrected policy stays;
-- rolling back the schema version leaves it in place.
DO $$ BEGIN
  RAISE NOTICE '000135 down: the invitation INSERT policy stays bound to the invitation''s own tenant (security fix, not reverted)';
END $$;
