-- ADR-0048 H4: outbound webhooks. Additive. A subscription belongs to ONE installation (so to one company), points to an HTTPS endpoint, and signs every call with its own
-- secret (stored encrypted with the platform key, ADR-0009; shown once). A delivery is one fact for one subscription: at-least-once, with retry and a dead-letter state.
-- Nothing here is readable or writable by a system session except through the SECURITY DEFINER functions the delivery worker uses.
SET LOCAL lock_timeout = '5s';

CREATE TABLE webhook_subscriptions (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL,
  installation_id UUID NOT NULL,
  url TEXT NOT NULL CHECK (char_length(url) BETWEEN 12 AND 2000 AND url ~ '^https://'),
  event_types TEXT[] NOT NULL CHECK (cardinality(event_types) BETWEEN 1 AND 7 AND event_types <@ ARRAY[
    'conversation.created.v1', 'conversation.control_changed.v1', 'conversation.closed.v1',
    'message.created.v1', 'message.status_changed.v1', 'contact.updated.v1', 'ticket.linked.v1']::TEXT[]),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
  key_id TEXT NOT NULL CHECK (key_id ~ '^whk_[a-z0-9]{12}$'),                -- public: sent in X-Omnira-Key-ID so the receiver picks the secret
  secret_ciphertext BYTEA NOT NULL,
  previous_key_id TEXT CHECK (previous_key_id IS NULL OR previous_key_id ~ '^whk_[a-z0-9]{12}$'),   -- rotation: the old secret stays valid for a short window
  previous_secret_ciphertext BYTEA,
  previous_valid_until TIMESTAMPTZ,
  enqueued_through_seq BIGINT NOT NULL DEFAULT 0,                            -- every fact up to this number already has (or needs no) delivery for this subscription
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  last_delivery_at TIMESTAMPTZ,
  last_http_status INTEGER,
  last_error_class TEXT,
  created_by UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, installation_id) REFERENCES external_app_installations (tenant_id, id) ON DELETE CASCADE,
  CHECK ((previous_secret_ciphertext IS NULL) = (previous_key_id IS NULL) AND (previous_key_id IS NULL) = (previous_valid_until IS NULL))
);
CREATE INDEX idx_webhook_subscriptions_installation ON webhook_subscriptions (tenant_id, installation_id);
CREATE INDEX idx_webhook_subscriptions_active ON webhook_subscriptions (status) WHERE status = 'active';

CREATE TABLE webhook_deliveries (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),                            -- the delivery_id the receiver gets: stable across the retries of one fact
  tenant_id UUID NOT NULL,
  subscription_id UUID NOT NULL,
  event_id UUID NOT NULL,
  event_seq BIGINT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'dead', 'cancelled')),
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  first_attempt_at TIMESTAMPTZ,
  last_attempt_at TIMESTAMPTZ,
  delivered_at TIMESTAMPTZ,
  last_http_status INTEGER,
  last_error_class TEXT,                                                     -- a closed vocabulary: timeout | connect | tls | blocked_address | http_status | redirect | not_visible | revoked | expired
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (subscription_id, event_seq),
  FOREIGN KEY (tenant_id, subscription_id) REFERENCES webhook_subscriptions (tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE webhook_deliveries SET (autovacuum_analyze_scale_factor = 0.02, autovacuum_vacuum_scale_factor = 0.05);   -- rows change status in bulk
CREATE INDEX idx_webhook_deliveries_due ON webhook_deliveries (next_attempt_at) WHERE status = 'pending';
CREATE INDEX idx_webhook_deliveries_subscription ON webhook_deliveries (tenant_id, subscription_id, status, created_at DESC);

DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['webhook_subscriptions', 'webhook_deliveries'] LOOP
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OF tenant_id ON %I FOR EACH ROW EXECUTE FUNCTION forbid_tenant_change()', t || '_tenant_immutable', t);
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY %I ON %I FOR SELECT USING (actor_has_permission(tenant_id, current_user_id(), 'integration.read'))$p$, t || '_read', t);
  END LOOP;
END $$;
-- At most five subscriptions per installation, decided under a lock so that concurrent creations cannot all pass the count.
CREATE FUNCTION webhook_subscriptions_limit() RETURNS TRIGGER LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended('omnira.webhook_subscriptions:' || NEW.installation_id::text, 0));
  IF (SELECT count(*) FROM webhook_subscriptions WHERE tenant_id = NEW.tenant_id AND installation_id = NEW.installation_id) >= 5 THEN
    RAISE EXCEPTION 'at most 5 webhook subscriptions per installation' USING ERRCODE = 'OM006';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER webhook_subscriptions_limit_trg BEFORE INSERT ON webhook_subscriptions FOR EACH ROW EXECUTE FUNCTION webhook_subscriptions_limit();
-- writing the subscription is the administrator's (integration.manage); writing a delivery is the worker's, through the functions below
CREATE POLICY webhook_subscriptions_insert ON webhook_subscriptions FOR INSERT WITH CHECK (actor_has_permission(tenant_id, current_user_id(), 'integration.manage'));
CREATE POLICY webhook_subscriptions_update ON webhook_subscriptions FOR UPDATE USING (actor_has_permission(tenant_id, current_user_id(), 'integration.manage'))
  WITH CHECK (actor_has_permission(tenant_id, current_user_id(), 'integration.manage'));
CREATE POLICY webhook_subscriptions_delete ON webhook_subscriptions FOR DELETE USING (actor_has_permission(tenant_id, current_user_id(), 'integration.manage'));
-- the administrator may re-queue a dead delivery (retry) and nothing else about it
CREATE POLICY webhook_deliveries_update ON webhook_deliveries FOR UPDATE USING (actor_has_permission(tenant_id, current_user_id(), 'integration.manage'))
  WITH CHECK (actor_has_permission(tenant_id, current_user_id(), 'integration.manage'));

REVOKE ALL ON webhook_subscriptions, webhook_deliveries FROM PUBLIC, omnira_app;
GRANT INSERT, DELETE ON webhook_subscriptions TO omnira_app;
GRANT UPDATE (url, event_types, status, updated_at) ON webhook_subscriptions TO omnira_app;
-- the secret never travels back to the administrator's screen: no SELECT on the ciphertext columns (a rotation goes through webhook_rotate_secret)
GRANT SELECT (id, tenant_id, installation_id, url, event_types, status, key_id, previous_key_id, previous_valid_until, enqueued_through_seq, consecutive_failures,
              last_delivery_at, last_http_status, last_error_class, created_by, created_at, updated_at) ON webhook_subscriptions TO omnira_app;
GRANT SELECT ON webhook_deliveries TO omnira_app;
GRANT UPDATE (status, next_attempt_at, attempts, first_attempt_at) ON webhook_deliveries TO omnira_app;

-- ---------------------------------------------------------------------------------------------------------------------------- worker doors
-- (1) Fan-out: give every active subscription a delivery row for the facts it asked for, in sequence order, a bounded batch at a time.
CREATE FUNCTION webhook_fanout(p_batch INTEGER DEFAULT 200)
RETURNS INTEGER
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  sub RECORD;
  created INTEGER := 0;
  top BIGINT;
  n INTEGER;
BEGIN
  IF p_batch < 1 OR p_batch > 2000 THEN RAISE EXCEPTION 'batch size out of range'; END IF;
  FOR sub IN
    SELECT s.id, s.tenant_id, s.event_types, s.enqueued_through_seq
      FROM webhook_subscriptions s
      JOIN external_app_installations i ON i.tenant_id = s.tenant_id AND i.id = s.installation_id AND i.status = 'active'
     WHERE s.status = 'active'
     ORDER BY s.id FOR UPDATE OF s SKIP LOCKED
  LOOP
    SELECT max(seq) INTO top FROM (SELECT seq FROM domain_events WHERE tenant_id = sub.tenant_id AND seq > sub.enqueued_through_seq AND seq IS NOT NULL ORDER BY seq LIMIT p_batch) w;
    CONTINUE WHEN top IS NULL;
    INSERT INTO webhook_deliveries(tenant_id, subscription_id, event_id, event_seq)
      SELECT e.tenant_id, sub.id, e.id, e.seq FROM domain_events e
       WHERE e.tenant_id = sub.tenant_id AND e.seq > sub.enqueued_through_seq AND e.seq <= top AND e.event_type = ANY (sub.event_types)
      ON CONFLICT (subscription_id, event_seq) DO NOTHING;
    GET DIAGNOSTICS n = ROW_COUNT;
    created := created + n;
    UPDATE webhook_subscriptions SET enqueued_through_seq = top WHERE id = sub.id;
  END LOOP;
  RETURN created;
END $$;

-- (2) Claim due deliveries. A claim moves next_attempt_at forward by the lease, so a worker that dies hands the delivery back by itself. Only deliveries of a live
-- installation, an active subscription and an active company are returned; the others are cancelled here (revocation reaches pending deliveries at once).
CREATE FUNCTION webhook_claim(p_limit INTEGER, p_lease INTERVAL)
RETURNS TABLE (delivery_id UUID, tenant_id UUID, subscription_id UUID, installation_id UUID, attempts INTEGER, first_attempt_at TIMESTAMPTZ,
               url TEXT, key_id TEXT, secret_ciphertext BYTEA, previous_key_id TEXT, previous_secret_ciphertext BYTEA, previous_valid_until TIMESTAMPTZ,
               event_id UUID, event_seq BIGINT, event_type TEXT, event_version INTEGER, aggregate_type TEXT, aggregate_id UUID, conversation_id UUID,
               aggregate_version BIGINT, origin_kind TEXT, origin_installation_id UUID, occurred_at TIMESTAMPTZ, payload JSONB)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF p_limit < 1 OR p_limit > 500 THEN RAISE EXCEPTION 'batch size out of range'; END IF;
  -- deliveries that can no longer be made
  -- (a paused subscription keeps its pending deliveries: they go out after it is resumed. A purged event can never be sent: the delivery ends instead of waiting for ever.)
  UPDATE webhook_deliveries d SET status = 'cancelled',
         last_error_class = CASE WHEN EXISTS (SELECT 1 FROM domain_events e WHERE e.id = d.event_id AND e.tenant_id = d.tenant_id) THEN 'revoked' ELSE 'expired' END
   WHERE d.status = 'pending' AND d.next_attempt_at <= now()
     AND (NOT EXISTS (SELECT 1 FROM webhook_subscriptions s
                       JOIN external_app_installations i ON i.tenant_id = s.tenant_id AND i.id = s.installation_id AND i.status = 'active'
                       JOIN tenants t ON t.id = s.tenant_id AND t.status = 'active'
                      WHERE s.id = d.subscription_id AND s.tenant_id = d.tenant_id)
          OR NOT EXISTS (SELECT 1 FROM domain_events e WHERE e.id = d.event_id AND e.tenant_id = d.tenant_id));
  RETURN QUERY
  WITH due AS (
    SELECT d.id FROM webhook_deliveries d
     WHERE d.status = 'pending' AND d.next_attempt_at <= now()
       AND EXISTS (SELECT 1 FROM webhook_subscriptions s WHERE s.id = d.subscription_id AND s.tenant_id = d.tenant_id AND s.status = 'active')
     ORDER BY d.next_attempt_at, d.event_seq LIMIT p_limit FOR UPDATE OF d SKIP LOCKED),
  claimed AS (
    UPDATE webhook_deliveries d SET next_attempt_at = now() + p_lease, attempts = d.attempts + 1, last_attempt_at = now(),
                                   first_attempt_at = COALESCE(d.first_attempt_at, now())
      FROM due WHERE d.id = due.id
    RETURNING d.id, d.tenant_id, d.subscription_id, d.attempts, d.first_attempt_at, d.event_id, d.event_seq)
  SELECT c.id, c.tenant_id, c.subscription_id, s.installation_id, c.attempts, c.first_attempt_at,
         s.url, s.key_id, s.secret_ciphertext, s.previous_key_id, s.previous_secret_ciphertext, s.previous_valid_until,
         e.id, e.seq, e.event_type, e.event_version, e.aggregate_type, e.aggregate_id, e.conversation_id,
         e.aggregate_version, e.origin_kind, e.origin_installation_id, e.occurred_at, e.payload
    FROM claimed c
    JOIN webhook_subscriptions s ON s.id = c.subscription_id AND s.tenant_id = c.tenant_id
    JOIN domain_events e ON e.id = c.event_id AND e.tenant_id = c.tenant_id
   ORDER BY c.event_seq;
END $$;

-- (3) Record the outcome of one attempt. ok -> delivered; otherwise retry at p_retry_at, or dead when the retry window is over (p_give_up).
CREATE FUNCTION webhook_complete(p_delivery UUID, p_tenant UUID, p_attempt INTEGER, p_ok BOOLEAN, p_http_status INTEGER, p_error_class TEXT, p_retry_at TIMESTAMPTZ, p_give_up BOOLEAN)
RETURNS VOID
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE sub UUID; n INTEGER;
BEGIN
  -- fencing: only the attempt that was claimed may record its outcome (a worker whose lease ran out and whose delivery was claimed again by another is ignored)
  SELECT d.subscription_id INTO sub FROM webhook_deliveries d WHERE d.id = p_delivery AND d.tenant_id = p_tenant AND d.status = 'pending';
  IF sub IS NULL THEN RETURN; END IF;
  -- lock order: subscription first, then the delivery (the order the fan-out takes)
  PERFORM 1 FROM webhook_subscriptions s WHERE s.id = sub AND s.tenant_id = p_tenant FOR UPDATE;
  UPDATE webhook_deliveries d SET
      status = CASE WHEN p_ok THEN 'delivered' WHEN p_error_class = 'not_visible' THEN 'cancelled' WHEN p_give_up THEN 'dead' ELSE 'pending' END,
      delivered_at = CASE WHEN p_ok THEN now() END,
      next_attempt_at = CASE WHEN p_ok OR p_give_up THEN d.next_attempt_at ELSE COALESCE(p_retry_at, now() + interval '1 minute') END,
      last_http_status = p_http_status,
      last_error_class = CASE WHEN p_ok THEN NULL ELSE left(p_error_class, 40) END
   WHERE d.id = p_delivery AND d.tenant_id = p_tenant AND d.status = 'pending' AND d.attempts = p_attempt;
  GET DIAGNOSTICS n = ROW_COUNT;
  IF n = 1 THEN
    UPDATE webhook_subscriptions SET last_delivery_at = now(), last_http_status = p_http_status,
           last_error_class = CASE WHEN p_ok THEN NULL ELSE left(p_error_class, 40) END,
           consecutive_failures = CASE WHEN p_ok THEN 0 ELSE consecutive_failures + 1 END
     WHERE id = sub AND tenant_id = p_tenant;
  END IF;
END $$;

-- (3b) Just before a call: is the subscription still active? (a pause lands between the claim and the send; NULL when it was deleted)
CREATE FUNCTION webhook_subscription_active(p_tenant UUID, p_subscription UUID)
RETURNS BOOLEAN
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$ SELECT s.status = 'active' FROM webhook_subscriptions s WHERE s.id = p_subscription AND s.tenant_id = p_tenant $$;
REVOKE ALL ON FUNCTION webhook_subscription_active(UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION webhook_subscription_active(UUID, UUID) TO omnira_app;

-- (4) How far behind the delivery is (alerts): pending deliveries and the age of the oldest one that is due.
CREATE FUNCTION webhook_backlog(OUT pending BIGINT, OUT dead BIGINT, OUT oldest_due_seconds DOUBLE PRECISION)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$ SELECT count(*) FILTER (WHERE status = 'pending'), count(*) FILTER (WHERE status = 'dead'),
              COALESCE(EXTRACT(EPOCH FROM now() - min(next_attempt_at) FILTER (WHERE status = 'pending' AND next_attempt_at <= now())), 0) FROM webhook_deliveries $$;

REVOKE ALL ON FUNCTION webhook_fanout(INTEGER), webhook_claim(INTEGER, INTERVAL), webhook_complete(UUID, UUID, INTEGER, BOOLEAN, INTEGER, TEXT, TIMESTAMPTZ, BOOLEAN), webhook_backlog() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION webhook_fanout(INTEGER), webhook_claim(INTEGER, INTERVAL), webhook_complete(UUID, UUID, INTEGER, BOOLEAN, INTEGER, TEXT, TIMESTAMPTZ, BOOLEAN), webhook_backlog() TO omnira_app;

-- (5) Rotation of the signing secret: the current one becomes the previous one (valid for p_keep), the new one becomes current. The ciphertext of the old secret is never
-- readable by the caller, so this is a function; it checks the permission itself because it runs with the owner's rights.
CREATE FUNCTION webhook_rotate_secret(p_tenant UUID, p_subscription UUID, p_key_id TEXT, p_ciphertext BYTEA, p_keep INTERVAL)
RETURNS BOOLEAN
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF NOT actor_has_permission(p_tenant, current_user_id(), 'integration.manage') THEN RETURN FALSE; END IF;
  IF p_keep < interval '1 minute' OR p_keep > interval '7 days' THEN RAISE EXCEPTION 'keep window out of range'; END IF;
  UPDATE webhook_subscriptions SET
      previous_key_id = key_id, previous_secret_ciphertext = secret_ciphertext, previous_valid_until = now() + p_keep,
      key_id = p_key_id, secret_ciphertext = p_ciphertext, updated_at = now()
   WHERE id = p_subscription AND tenant_id = p_tenant;
  RETURN FOUND;
END $$;
REVOKE ALL ON FUNCTION webhook_rotate_secret(UUID, UUID, TEXT, BYTEA, INTERVAL) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION webhook_rotate_secret(UUID, UUID, TEXT, BYTEA, INTERVAL) TO omnira_app;

-- (6) Where the journal is NOW, for a new subscription to start from (it does not replay history unless the application asks for the journal itself). The cursor
-- table is not readable by a person, so this door answers for the administrator of that company only.
CREATE FUNCTION webhook_journal_head(p_tenant UUID)
RETURNS BIGINT
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF NOT actor_has_permission(p_tenant, current_user_id(), 'integration.manage') THEN RETURN NULL; END IF;
  RETURN COALESCE((SELECT last_seq FROM domain_event_cursors WHERE tenant_id = p_tenant), 0);
END $$;
REVOKE ALL ON FUNCTION webhook_journal_head(UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION webhook_journal_head(UUID) TO omnira_app;
