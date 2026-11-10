DROP INDEX IF EXISTS idx_wa_groups_one_alert_channel_per_tenant;
ALTER TABLE wa_groups DROP CONSTRAINT IF EXISTS wa_groups_alert_channel_not_client_group;
ALTER TABLE wa_groups DROP COLUMN IF EXISTS alert_channel_set_at;
ALTER TABLE wa_groups DROP COLUMN IF EXISTS alert_channel_set_by;
ALTER TABLE wa_groups DROP COLUMN IF EXISTS is_alert_channel;
