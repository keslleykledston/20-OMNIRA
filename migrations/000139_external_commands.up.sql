-- ADR-0046 H5: coordinated writes by an external application.
--  * external_command_receipts: one row per accepted command, written in the transaction that performed it. Scope (tenant, installation, operation, key); a repeat with the same
--    key and the same canonical payload gets the stored answer, a different payload is refused. Failures leave no receipt (their transaction rolls back).
--  * conversation_closures learns that an application can close an attendance: source 'external_app' and the installation (never a user).
SET LOCAL lock_timeout = '5s';

CREATE TABLE external_command_receipts (
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  installation_id UUID NOT NULL,
  operation TEXT NOT NULL CHECK (operation IN ('claim', 'release', 'transfer', 'message', 'finalize')),
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

-- an application can close an attendance (the same closure record as a person or the system, attributed to the installation)
ALTER TABLE conversation_closures DROP CONSTRAINT conversation_closures_source_check;
ALTER TABLE conversation_closures ADD CONSTRAINT conversation_closures_source_check CHECK (source IN ('agent', 'supervisor', 'system', 'external_app'));
ALTER TABLE conversation_closures ADD COLUMN closed_by_installation_id UUID;
ALTER TABLE conversation_closures ADD CONSTRAINT conversation_closures_installation_fk
  FOREIGN KEY (tenant_id, closed_by_installation_id) REFERENCES external_app_installations (tenant_id, id);
ALTER TABLE conversation_closures ADD CONSTRAINT conversation_closures_closer_shape
  CHECK ((source = 'external_app') = (closed_by_installation_id IS NOT NULL) AND (closed_by_installation_id IS NULL OR closed_by_user_id IS NULL));
