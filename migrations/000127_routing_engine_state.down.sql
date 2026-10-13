-- OPERATIONAL GATE: take a backup and get the owner's approval first. DATA LOSS: drops the routing state, the decision history and the timers. Queues in least_loaded mode are put back to round_robin first
-- so the old CHECK holds. Rolling back the code does not need this (the old code ignores these tables).
UPDATE queues SET mode = 'round_robin' WHERE mode = 'least_loaded';
ALTER TABLE queues DROP CONSTRAINT IF EXISTS queues_mode_check;
ALTER TABLE queues ADD CONSTRAINT queues_mode_check CHECK (mode IN ('manual', 'round_robin'));
DROP TABLE IF EXISTS routing_engine_timers;
DROP TABLE IF EXISTS routing_engine_decisions;
DROP TABLE IF EXISTS routing_engine_state;
