-- fixture (F4, Codex round 4): "DEFAULT NULL" supplies no real value to an older release's
-- INSERT that omits this column — it must not be treated as a compatible default.
ALTER TABLE a ADD COLUMN x text NOT NULL DEFAULT NULL;
