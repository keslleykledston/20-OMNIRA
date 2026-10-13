-- OPERATIONAL GATE: backup and the owner's approval first. Steps that use the new destinations are removed (the old CHECK could not hold them).
DELETE FROM overflow_steps WHERE destination_kind IN ('agent', 'sector', 'hub');
DROP FUNCTION IF EXISTS hub_destination_ready(UUID, UUID, UUID);
DROP FUNCTION IF EXISTS tenant_hub_contracts(UUID);
ALTER TABLE overflow_steps DROP CONSTRAINT IF EXISTS overflow_steps_dest_queue_fk;
ALTER TABLE overflow_steps DROP CONSTRAINT IF EXISTS overflow_steps_hub_queue_chk;
ALTER TABLE overflow_steps DROP COLUMN IF EXISTS destination_queue_id;
ALTER TABLE overflow_steps DROP CONSTRAINT IF EXISTS overflow_steps_destination_kind_check;
ALTER TABLE overflow_steps ADD CONSTRAINT overflow_steps_destination_kind_check CHECK (destination_kind IN ('queue', 'oncall'));
