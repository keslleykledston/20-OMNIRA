ALTER TABLE user_identities DROP COLUMN IF EXISTS email_verified;
ALTER TABLE membership_invitations DROP COLUMN IF EXISTS sent_at;
