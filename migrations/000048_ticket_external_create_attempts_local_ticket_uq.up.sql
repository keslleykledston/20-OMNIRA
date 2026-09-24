-- PRODUCT.6-M5: closes the cross-Idempotency-Key race left open by
-- 000047. Two concurrent requests with DIFFERENT (both individually
-- valid) Idempotency-Keys must not both be able to acquire permission to
-- call the external provider's CreateTicket for the SAME local ticket —
-- a process-local mutex or a check-then-insert application query cannot
-- guarantee this across separate Go processes/browsers/devices; only a
-- database-level uniqueness invariant can.
--
-- confirmed_failure is deliberately EXCLUDED from this index: the
-- provider definitively rejected that attempt, so it must not
-- permanently block a later, corrected create intent for the same local
-- ticket (PRODUCT.6-M5 section 4/9).
CREATE UNIQUE INDEX ticket_external_create_attempts_blocking_local_ticket_uq
  ON ticket_external_create_attempts(tenant_id, local_ticket_id)
  WHERE local_ticket_id IS NOT NULL AND state IN ('in_flight', 'confirmed_success', 'outcome_unknown');
