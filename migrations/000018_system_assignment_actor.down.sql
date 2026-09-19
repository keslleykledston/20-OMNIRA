DO $rollback$
BEGIN
  IF EXISTS (SELECT 1 FROM assignment_events WHERE actor_source = 'system') THEN
    RAISE EXCEPTION 'cannot rollback 000018 while system assignment history exists';
  END IF;
END $rollback$;
ALTER TABLE assignment_events DROP CONSTRAINT IF EXISTS assignment_events_actor_shape;
ALTER TABLE assignment_events ALTER COLUMN changed_by SET NOT NULL;
ALTER TABLE assignment_events DROP COLUMN IF EXISTS actor_source;
