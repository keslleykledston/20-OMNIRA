ALTER TABLE membership_invitations
  DROP CONSTRAINT IF EXISTS membership_invitations_queues_need_attends,
  DROP CONSTRAINT IF EXISTS membership_invitations_queue_capacity_range,
  DROP COLUMN IF EXISTS queue_capacity,
  DROP COLUMN IF EXISTS queue_ids,
  DROP COLUMN IF EXISTS attends;
