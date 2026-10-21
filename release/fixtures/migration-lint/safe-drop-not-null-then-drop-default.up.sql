-- fixture (F4, Codex round 9): DROP NOT NULL loosens a column back to nullable — a later DROP
-- DEFAULT on it is then fine, since an older release omitting it just gets NULL again.
ALTER TABLE t ADD COLUMN c integer NOT NULL DEFAULT 1;
ALTER TABLE t ALTER COLUMN c DROP NOT NULL;
ALTER TABLE t ALTER COLUMN c DROP DEFAULT;
