-- fixture (F4, Codex round 3): a DEFAULT given to one table's column must never exempt a SET
-- NOT NULL on a DIFFERENT table's same-named column.
ALTER TABLE new_table ADD COLUMN status text DEFAULT 'new';
ALTER TABLE conversations ALTER COLUMN status SET NOT NULL;
