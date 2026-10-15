-- Drops the attendance plan of every invitation, pending ones included: after this, a pending invitation is accepted WITHOUT a plan
-- (the person joins with the role only; queues are chosen in the sheet). Export the plans first if they matter.
ALTER TABLE membership_invitations
  DROP CONSTRAINT IF EXISTS membership_invitations_queues_need_attends,
  DROP CONSTRAINT IF EXISTS membership_invitations_queue_capacity_range,
  DROP COLUMN IF EXISTS queue_capacity,
  DROP COLUMN IF EXISTS queue_ids,
  DROP COLUMN IF EXISTS attends;
