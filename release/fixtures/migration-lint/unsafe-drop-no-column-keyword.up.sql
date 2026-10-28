-- fixture (F4, Codex round 5): Postgres allows omitting the COLUMN keyword on DROP (and ADD)
-- inside ALTER TABLE. A destructive column drop must be caught either way.
ALTER TABLE accounts DROP email;
