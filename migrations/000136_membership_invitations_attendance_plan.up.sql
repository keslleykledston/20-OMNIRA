-- ADR-0046: the invitation carries what the person will do once inside, applied when it is accepted (same transaction as the
-- membership): whether they attend and in which queues of THIS tenant, with one capacity for each. The queue ids are checked
-- against the tenant when the invitation is created and again when it is applied (a queue deleted meanwhile is skipped).
-- The existing row policies cover the new columns.
ALTER TABLE membership_invitations
  ADD COLUMN attends boolean NOT NULL DEFAULT false,
  ADD COLUMN queue_ids uuid[] NOT NULL DEFAULT '{}',
  ADD COLUMN queue_capacity integer NOT NULL DEFAULT 1,
  ADD CONSTRAINT membership_invitations_queue_capacity_range CHECK (queue_capacity BETWEEN 1 AND 100),
  ADD CONSTRAINT membership_invitations_queues_need_attends CHECK (attends OR cardinality(queue_ids) = 0);
