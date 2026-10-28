-- fixture (F4, Codex round 10): "DEFAULT (NULL)" (parenthesized) is the same disguised-NULL
-- default as the bare form — must not exempt a NOT NULL column.
ALTER TABLE accounts ADD COLUMN state integer NOT NULL DEFAULT (NULL);
