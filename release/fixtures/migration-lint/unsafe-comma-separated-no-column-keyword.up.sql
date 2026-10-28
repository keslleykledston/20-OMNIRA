-- fixture (F4, Codex round 6): the span-stop delimiter must recognize ADD/DROP/ALTER whether or
-- not COLUMN follows — otherwise a later column's DEFAULT in the same comma list bleeds back
-- and wrongly exempts an earlier column's inline NOT NULL.
ALTER TABLE accounts ADD required text NOT NULL, ADD optional text DEFAULT 'ok';
