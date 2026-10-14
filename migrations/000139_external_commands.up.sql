-- ADR-0046 H5: coordinated writes by an external application.
--  * external_command_receipts: one row per accepted command, written in the transaction that performed it. Scope (tenant, installation, operation, key); a repeat with the same
--    key and the same canonical payload gets the stored answer, a different payload is refused. Failures leave no receipt (their transaction rolls back).
--  * conversation_closures learns that an application can close an attendance: source 'external_app' and the installation (never a user).
SET LOCAL lock_timeout = '5s';

CREATE TABLE external_command_receipts (
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  installation_id UUID NOT NULL,
  operation TEXT NOT NULL CHECK (operation IN ('claim', 'release', 'transfer', 'message', 'finalize', 'heartbeat')),
  idempotency_key TEXT NOT NULL CHECK (idempotency_key ~ '^[A-Za-z0-9._:-]{8,128}$'),
  request_hash TEXT NOT NULL CHECK (char_length(request_hash) = 64),
  conversation_id UUID NOT NULL,
  message_id UUID,                                   -- the message a send queued, so a replay returns the same id
  control_version BIGINT,
  status_code INTEGER NOT NULL CHECK (status_code BETWEEN 200 AND 299),
  response JSONB NOT NULL,                           -- the exact body that was answered
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, installation_id, operation, idempotency_key),
  FOREIGN KEY (tenant_id, installation_id) REFERENCES external_app_installations (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX external_command_receipts_age_idx ON external_command_receipts (created_at);
CREATE TRIGGER external_command_receipts_tenant_immutable BEFORE UPDATE OF tenant_id ON external_command_receipts
  FOR EACH ROW EXECUTE FUNCTION forbid_tenant_change();
ALTER TABLE external_command_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE external_command_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY external_command_receipts_system ON external_command_receipts FOR ALL USING (is_system_admin()) WITH CHECK (is_system_admin());
REVOKE ALL ON external_command_receipts FROM PUBLIC, omnira_app;
GRANT SELECT, INSERT ON external_command_receipts TO omnira_app;   -- append-only: no UPDATE, no DELETE (the purge is a function)

-- Receipts only have to outlive the longest sensible retry of a client; the cleanup keeps them for p_keep (never less than a day).
CREATE FUNCTION external_command_receipts_purge(p_keep INTERVAL)
RETURNS BIGINT
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE n BIGINT;
BEGIN
  IF p_keep < interval '1 day' THEN RAISE EXCEPTION 'receipt retention shorter than one day refused'; END IF;
  DELETE FROM external_command_receipts WHERE created_at < now() - p_keep;
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n;
END $$;
REVOKE ALL ON FUNCTION external_command_receipts_purge(INTERVAL) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION external_command_receipts_purge(INTERVAL) TO omnira_app;

-- The list of the external API is keyset-paged by created_at (the default) or updated_at (an index for it exists since migration 14). Without an index on created_at the
-- database sorts every visible conversation of the company for each page and evaluates the row-level-security policies on all of them (measured: ~1.4 s per page at 30 000
-- conversations); with it the scan stops after one page.
CREATE INDEX idx_conversations_tenant_created ON conversations (tenant_id, created_at DESC, id DESC);

-- The CRM lease (ADR-0046 §6.1): optional per installation (external_app_installations.lease_seconds; NULL = the application holds until it releases). While the
-- installation holds a conversation, every command it makes and its heartbeat push the expiry forward; a hold whose lease (plus the grace period) ran out is freed by the
-- worker with the reason lease_expired and the router looks at the conversation again. A person is never expired.
CREATE TABLE external_conversation_leases (
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id UUID NOT NULL,
  installation_id UUID NOT NULL,
  lease_until TIMESTAMPTZ NOT NULL,
  renewed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, conversation_id),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, installation_id) REFERENCES external_app_installations (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX external_conversation_leases_due_idx ON external_conversation_leases (lease_until);
CREATE TRIGGER external_conversation_leases_tenant_immutable BEFORE UPDATE OF tenant_id ON external_conversation_leases
  FOR EACH ROW EXECUTE FUNCTION forbid_tenant_change();
ALTER TABLE external_conversation_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE external_conversation_leases FORCE ROW LEVEL SECURITY;
CREATE POLICY external_conversation_leases_system ON external_conversation_leases FOR ALL USING (is_system_admin()) WITH CHECK (is_system_admin());
REVOKE ALL ON external_conversation_leases FROM PUBLIC, omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON external_conversation_leases TO omnira_app;

-- The worker's door to the leases that ran out (the table is readable only by a system session of ONE company, the worker has none yet).
CREATE FUNCTION external_leases_due(p_grace INTERVAL, p_limit INTEGER)
RETURNS TABLE (tenant_id UUID, conversation_id UUID, installation_id UUID)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
  SELECT l.tenant_id, l.conversation_id, l.installation_id
    FROM external_conversation_leases l
   WHERE l.lease_until + p_grace < now()
   ORDER BY l.lease_until
   LIMIT LEAST(GREATEST(p_limit, 1), 500)
$$;
REVOKE ALL ON FUNCTION external_leases_due(INTERVAL, INTEGER) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION external_leases_due(INTERVAL, INTEGER) TO omnira_app;

-- an application can close an attendance (the same closure record as a person or the system, attributed to the installation)
ALTER TABLE conversation_closures DROP CONSTRAINT conversation_closures_source_check;
ALTER TABLE conversation_closures ADD CONSTRAINT conversation_closures_source_check CHECK (source IN ('agent', 'supervisor', 'system', 'external_app'));
ALTER TABLE conversation_closures ADD COLUMN closed_by_installation_id UUID;
ALTER TABLE conversation_closures ADD CONSTRAINT conversation_closures_installation_fk
  FOREIGN KEY (tenant_id, closed_by_installation_id) REFERENCES external_app_installations (tenant_id, id);
ALTER TABLE conversation_closures ADD CONSTRAINT conversation_closures_closer_shape
  CHECK ((source = 'external_app') = (closed_by_installation_id IS NOT NULL) AND (closed_by_installation_id IS NULL OR closed_by_user_id IS NULL));
