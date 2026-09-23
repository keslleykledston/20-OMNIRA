-- PRODUCT.6-D (ADR-0013): additive foundation for representing a
-- tenant-ERP-backed ticket as a local projection/link, before any real
-- connector exists. Every column is nullable — existing rows (and every
-- future automatic-inbound local-only ticket, still transitional per
-- ADR-0013) remain valid with all six columns NULL. No default is set
-- deliberately: NULL means "no external projection", not "unknown provider".
--
--   provider:              which external ticketing system owns this ticket
--                          (e.g. 'k3g_crm'); NULL for local-only tickets.
--   external_ticket_id:    the provider's stable ticket identifier, TEXT
--                          even though the currently-confirmed K3G contract
--                          returns a numeric id — other providers (IXC, SGP,
--                          HubSoft, ...) may use non-numeric identifiers.
--   external_status:       raw provider status/code, as returned by the
--                          provider (e.g. K3G's statusId "1"), never
--                          normalized here.
--   external_status_label: raw provider human-readable status label (e.g.
--                          K3G's "Novo"), for operator display only.
--   sync_status:           reserved for projection synchronization truth.
--                          Deliberately no CHECK/enum yet — the consistency
--                          execution model (synchronous vs. outbox, retry
--                          semantics) is still TBD per ADR-0013 §7; freezing
--                          values now would encode a mechanism decision this
--                          migration does not make.
--   last_synced_at:        timestamp of the last successful projection sync
--                          with the provider.
ALTER TABLE tickets
  ADD COLUMN provider TEXT,
  ADD COLUMN external_ticket_id TEXT,
  ADD COLUMN external_status TEXT,
  ADD COLUMN external_status_label TEXT,
  ADD COLUMN sync_status TEXT,
  ADD COLUMN last_synced_at TIMESTAMPTZ;

-- A tenant can only have one local ticket projecting a given external ticket
-- from a given provider. Partial index (WHERE both non-null) so legacy/local
-- rows — where both columns are NULL — never collide with each other or with
-- a real projection; Postgres treats every NULL as distinct in a unique
-- index, but the explicit WHERE clause documents the intent instead of
-- relying on that implicitly.
CREATE UNIQUE INDEX tickets_tenant_provider_external_id_uq
  ON tickets (tenant_id, provider, external_ticket_id)
  WHERE provider IS NOT NULL AND external_ticket_id IS NOT NULL;
