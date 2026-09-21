#!/usr/bin/env bash
# Evidência read-only de um inbound WAHA real: mensagem persistida, correlação e dedupe.
# Uso: scripts/p8-evidence.sh <texto-exato-ou-prefixo> [banco]   (default banco: omnira_dev)
# Só SELECT, dentro de BEGIN READ ONLY. Não imprime segredos nem payloads; telefone mascarado.
# Container Postgres: auto-detecta quem publica 127.0.0.1:55434 (override: E2E_PG_CONTAINER).
set -uo pipefail
PATTERN="${1:-}"; DB="${2:-omnira_dev}"
[ -n "$PATTERN" ] || { echo "uso: $0 <texto|prefixo> [banco]" >&2; exit 2; }
PG="${E2E_PG_CONTAINER:-$(docker ps --format '{{.Names}}\t{{.Ports}}' | awk '/:55434->/{print $1; exit}')}"
[ -n "$PG" ] || { echo "nenhum container publica 127.0.0.1:55434" >&2; exit 2; }

docker exec -i "$PG" psql -U omnira -d "$DB" -v ON_ERROR_STOP=1 -v pat="$PATTERN" -P pager=off <<'SQL'
BEGIN READ ONLY;
\echo == Q1 mensagem + correlação (RLS ignorada: owner, somente leitura)
SELECT m.id AS message_id, m.provider_message_id, m.direction, m.status, m.created_at,
       conv.id AS conversation_id, conv.contact_id,
       regexp_replace(ct.phone_e164, '(\d{4})\d+(\d{2})$', '\1****\2') AS contact_phone_masked,
       cc.id AS connection_id, cc.provider, cc.provider_kind, cc.status AS connection_status
  FROM messages m
  JOIN conversations conv ON conv.id = m.conversation_id
  JOIN contacts ct        ON ct.id = conv.contact_id
  LEFT JOIN channel_connections cc ON cc.id = COALESCE(m.channel_connection_id, conv.channel_connection_id)
 WHERE m.body LIKE :'pat' || '%'
 ORDER BY m.created_at DESC LIMIT 10;

\echo == Q2 dedupe exactly-once (por provider_message_id)
SELECT m.provider_message_id,
       (SELECT count(*) FROM channel_webhook_events e
         WHERE e.provider_event_id = m.provider_message_id) AS webhook_events,
       (SELECT count(*) FROM messages x
         WHERE x.provider_message_id = m.provider_message_id
           AND x.tenant_id = m.tenant_id) AS message_rows,
       CASE
         WHEN m.provider_message_id = '' THEN 'FAIL: provider_message_id vazio'
         WHEN (SELECT count(*) FROM messages x WHERE x.provider_message_id = m.provider_message_id AND x.tenant_id = m.tenant_id) <> 1 THEN 'FAIL: message_rows <> 1'
         WHEN (SELECT count(*) FROM channel_webhook_events e WHERE e.provider_event_id = m.provider_message_id) = 0 THEN 'WARN: sem webhook event registrado'
         WHEN (SELECT count(*) FROM channel_webhook_events e WHERE e.provider_event_id = m.provider_message_id) > 1 THEN 'FAIL: webhook duplicado'
         ELSE 'PASS: exactly-once'
       END AS dedupe_status
  FROM messages m
 WHERE m.body LIKE :'pat' || '%' AND m.direction = 'inbound'
 ORDER BY m.created_at DESC LIMIT 10;

\echo == Q3 eventos de webhook (sem payload)
SELECT e.provider_event_id, e.provider, e.event_type, e.received_at, e.connection_id
  FROM channel_webhook_events e
 WHERE e.provider_event_id IN (SELECT provider_message_id FROM messages WHERE body LIKE :'pat' || '%' AND provider_message_id <> '')
 ORDER BY e.received_at DESC LIMIT 10;
ROLLBACK;
SQL
