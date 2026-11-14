DROP INDEX IF EXISTS idx_channel_participants_company_account;

ALTER TABLE channel_participants
  DROP CONSTRAINT IF EXISTS channel_participants_profile_set_by_fkey,
  DROP CONSTRAINT IF EXISTS channel_participants_company_account_fk,
  DROP CONSTRAINT IF EXISTS channel_participants_company_role_requires_account,
  DROP CONSTRAINT IF EXISTS channel_participants_company_role_check,
  DROP CONSTRAINT IF EXISTS channel_participants_alias_check;

ALTER TABLE channel_participants
  DROP COLUMN IF EXISTS profile_set_at,
  DROP COLUMN IF EXISTS profile_set_by,
  DROP COLUMN IF EXISTS company_role,
  DROP COLUMN IF EXISTS company_account_id,
  DROP COLUMN IF EXISTS alias;
