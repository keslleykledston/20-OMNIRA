DELETE FROM overflow_steps WHERE destination_kind = 'flow';
ALTER TABLE overflow_steps DROP CONSTRAINT IF EXISTS overflow_steps_destination_kind_check;
ALTER TABLE overflow_steps ADD CONSTRAINT overflow_steps_destination_kind_check CHECK (destination_kind IN ('queue', 'oncall', 'agent', 'sector', 'hub'));
