-- fixture (F4, Codex round 6): DROP DEFAULT revokes the compatibility an earlier ADD COLUMN
-- ... DEFAULT gave a column — a later SET NOT NULL on it is no longer a safe expand.
ALTER TABLE accounts ADD COLUMN required text DEFAULT 'ok';
ALTER TABLE accounts ALTER COLUMN required DROP DEFAULT;
ALTER TABLE accounts ALTER COLUMN required SET NOT NULL;
