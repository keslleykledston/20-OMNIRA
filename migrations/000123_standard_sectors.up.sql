-- ADR-0043: every company starts with the standard sectors (a queue is a sector). Companies that exist today get them here;
-- companies created through the Hub get them in internal/hub/companies (StandardSectors). Manual mode (conversations wait to be
-- claimed), never the default queue - routing of new conversations does not change. A company that already has a queue with one
-- of these names keeps its own (mode, members and conversations untouched).
INSERT INTO queues (tenant_id, name, mode, is_default)
SELECT t.id, s.name, 'manual', false
FROM tenants t CROSS JOIN (VALUES ('Comercial'), ('Financeiro'), ('Suporte')) AS s(name)
ON CONFLICT (tenant_id, name) DO NOTHING;
