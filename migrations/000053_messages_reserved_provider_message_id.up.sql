-- PILOT.4A1: durable, pre-send reservation of the exact WAHA/GOWS message ID
-- that will be sent to the provider — separate from provider_message_id,
-- whose semantic contract remains unchanged: provider_message_id is only
-- ever set after a CONFIRMED successful provider response (MarkSent).
--
-- reserved_provider_message_id is written and committed in its OWN
-- transaction, BEFORE the first external SendText call. A worker crash after
-- a successful provider send but before the MarkSent transaction commits
-- leaves this reservation durable and visible to any redelivery: reuse the
-- exact same ID instead of minting a new one. WAHA/GOWS 2026.8.2 has been
-- runtime-proven (PILOT.4A0) to deduplicate a second send carrying an
-- identical WhatsApp message ID down to exactly one visible delivery.
--
-- Applies to outbound provider messages only; inbound rows, and outbound
-- rows never queued for delivery, simply never populate it.
ALTER TABLE messages
  ADD COLUMN reserved_provider_message_id TEXT NOT NULL DEFAULT '';

-- Mirrors messages_provider_connection_id_uq exactly: two different local
-- messages on the same connection must never reserve the same WAHA id.
CREATE UNIQUE INDEX messages_reserved_provider_connection_id_uq
  ON messages (tenant_id, channel_connection_id, reserved_provider_message_id)
  WHERE reserved_provider_message_id <> '' AND channel_connection_id IS NOT NULL;
