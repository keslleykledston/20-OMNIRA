-- Rolls back ADR-0049 phase 2. DESTRUCTIVE: the operational history written since is lost. Roll back 000152+ first.
SET LOCAL lock_timeout = '5s';
DROP TABLE IF EXISTS context_events;
DROP FUNCTION IF EXISTS context_events_immutable();
