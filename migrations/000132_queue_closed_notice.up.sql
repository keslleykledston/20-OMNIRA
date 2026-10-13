-- ADR-0044: an optional message to the customer when the group they reached is closed ("fora do expediente"). Opt-in: empty = say nothing,
-- exactly as before (a published flow that already tells the customer keeps doing so). The engine sends it at most once per routing
-- episode (idempotency key), and the text may contain {{proxima_abertura}}.
ALTER TABLE queues ADD COLUMN closed_notice TEXT NOT NULL DEFAULT '' CHECK (char_length(closed_notice) <= 500);
