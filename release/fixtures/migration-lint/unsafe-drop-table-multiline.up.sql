-- fixture (F4, Codex round 12): a destructive statement formatted across line breaks must not
-- evade detection just because plain `grep` matches one line at a time.
ALTER TABLE widgets ADD COLUMN notes text;
DROP
TABLE widgets;
