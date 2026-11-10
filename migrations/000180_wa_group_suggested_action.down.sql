ALTER TABLE wa_group_message_suggestions
  DROP CONSTRAINT IF EXISTS wa_group_message_suggestions_action_len;
ALTER TABLE wa_group_message_suggestions
  DROP COLUMN IF EXISTS suggested_action;
