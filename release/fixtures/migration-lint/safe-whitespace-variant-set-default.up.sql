-- fixture (F4, Codex round 9): irregular whitespace between keywords is valid SQL and must not
-- defeat the exact-string classification of the captured ALTER COLUMN action.
ALTER TABLE t ADD COLUMN c integer DEFAULT 1;
ALTER TABLE t ALTER COLUMN c SET  DEFAULT 2;
ALTER TABLE t ALTER COLUMN c SET NOT NULL;
