-- ADR-0044 (addendum): an optional message to the customer when the group they were waiting for OPENS and the conversation is released to
-- the queue ("já estamos atendendo"). Opt-in like closed_notice: empty = say nothing, exactly as before. The engine sends it at most once per
-- routing episode (idempotency key), only when a conversation that was waiting for the opening is woken up by its durable timer.
ALTER TABLE queues ADD COLUMN open_notice TEXT NOT NULL DEFAULT '' CHECK (char_length(open_notice) <= 500);
