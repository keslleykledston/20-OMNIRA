-- ADR-0044: a published automation flow as an overflow destination. The flow is checked when the policy is saved (it belongs to the company, is
-- published and of a startable type) and again live when the step is entered (it may have been archived since). No new table.
ALTER TABLE overflow_steps DROP CONSTRAINT IF EXISTS overflow_steps_destination_kind_check;
ALTER TABLE overflow_steps ADD CONSTRAINT overflow_steps_destination_kind_check CHECK (destination_kind IN ('queue', 'oncall', 'agent', 'sector', 'hub', 'flow'));
