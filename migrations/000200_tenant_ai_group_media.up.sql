-- ADR-0015 addendum 2 / ADR-0016: group image interpretation shares the tenant's existing Gemini key/budget/
-- consent, but is its OWN explicit decision — a tenant may want external AI reading 1:1 attachments and NOT
-- group images, or vice versa. Same "opt-in independente de tudo o mais" philosophy ADR-0055 already uses for
-- the group text observer. Can only be true while the base integration itself is enabled (same key to use).
ALTER TABLE tenant_ai_integrations ADD COLUMN group_media_enabled BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE tenant_ai_integrations ADD CONSTRAINT tenant_ai_group_media_requires_enabled
  CHECK (NOT group_media_enabled OR enabled);
