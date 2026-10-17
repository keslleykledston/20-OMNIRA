-- OPERATIONAL GATE: backup and the owner's approval first. DATA LOSS: drops every webhook subscription and the delivery history (including the dead-letter list).
-- The applications stop receiving calls; they can still read the journal by polling GET /external/events.
DROP FUNCTION IF EXISTS webhook_subscription_active(UUID, UUID);
DROP FUNCTION IF EXISTS webhook_journal_head(UUID);
DROP FUNCTION IF EXISTS webhook_rotate_secret(UUID, UUID, TEXT, BYTEA, INTERVAL);
DROP FUNCTION IF EXISTS webhook_backlog();
DROP FUNCTION IF EXISTS webhook_complete(UUID, UUID, INTEGER, BOOLEAN, INTEGER, TEXT, TIMESTAMPTZ, BOOLEAN);
DROP FUNCTION IF EXISTS webhook_claim(INTEGER, INTERVAL);
DROP FUNCTION IF EXISTS webhook_fanout(INTEGER);
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhook_subscriptions;
DROP FUNCTION IF EXISTS webhook_subscriptions_limit();
