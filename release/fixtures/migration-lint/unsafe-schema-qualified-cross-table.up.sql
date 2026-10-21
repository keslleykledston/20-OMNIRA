-- fixture (F4, Codex round 4): a schema-qualified table name (public.a) must not collapse onto
-- a different schema-qualified table (public.b) sharing the same schema prefix when checking
-- whether a DEFAULT exempts a later SET NOT NULL.
ALTER TABLE public.a ADD COLUMN x text DEFAULT 'ok';
ALTER TABLE public.b ALTER COLUMN x SET NOT NULL;
