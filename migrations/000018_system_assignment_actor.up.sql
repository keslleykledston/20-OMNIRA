-- M04.2a: assignment history distinguishes human claims from system routing.
ALTER TABLE assignment_events ADD COLUMN actor_source TEXT NOT NULL DEFAULT 'human'
  CHECK (actor_source IN ('human', 'system'));
ALTER TABLE assignment_events ALTER COLUMN changed_by DROP NOT NULL;
ALTER TABLE assignment_events ADD CONSTRAINT assignment_events_actor_shape
  CHECK (
    (actor_source = 'human' AND changed_by IS NOT NULL) OR
    (actor_source = 'system' AND changed_by IS NULL)
  );
