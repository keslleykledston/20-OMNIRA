# P8 — Evidência de banco: inbound WAHA persistido uma única vez

**Resultado: PASS.** Coletado em 2026-10-03, somente leitura.

Escopo: prova que a mensagem de teste `OMNIRA-E2E-P8-20260921-03` (inbound WAHA, 2026-09-21) foi persistida exatamente uma vez. Não declara produção pronta; os gates formais continuam em `docs/delivery/`.

## Onde está o banco

O OMNIRA roda neste host em Docker. A stack do vhost `omnira.devops.k3gsolutions.com.br` (`docker-compose.prod.yml`) usa o container `omnira-postgres` (127.0.0.1:55434), banco `omnira_dev`. Não existe banco remoto separado.

## Como consultar

O app (`omnira_app`) está sujeito a RLS e, sem tenant definido na sessão, enxerga 0 linhas — isso não significa banco vazio. Para inspeção, use o dono do banco (`omnira`, superuser, ignora RLS) e sempre em transação somente leitura:

```bash
docker compose exec -T postgres psql -U omnira -d omnira_dev
# dentro do psql: BEGIN READ ONLY; ...; COMMIT;
```

Nunca desabilitar RLS para consultar.

## Evidência

Query 1 — localizar a mensagem e os IDs correlacionados:

```sql
SELECT m.id AS message_id, m.provider_message_id, m.direction, m.status, m.created_at,
       c.id AS contact_id, c.phone_e164, conv.id AS conversation_id,
       cc.id AS channel_connection_id, cc.provider, cc.provider_kind,
       (SELECT count(*) FROM messages WHERE body = 'OMNIRA-E2E-P8-20260921-03') AS exact_content_count
FROM messages m
JOIN conversations conv ON conv.id = m.conversation_id
JOIN contacts c ON c.id = conv.contact_id
JOIN channel_connections cc ON cc.id = conv.channel_connection_id
WHERE m.body = 'OMNIRA-E2E-P8-20260921-03' AND cc.provider = 'waha';
```

| Campo | Valor |
|---|---|
| message_id | `f251034f-188e-4714-b54e-601b7efd8428` |
| provider_message_id | `false_175222334484588@lid_2A86D76A78DBD4A7EE68` |
| direction / status | inbound / received |
| created_at | 2026-09-21 18:34:10 UTC (14:34 em Manaus, UTC-4) |
| contact | `c328df30-e628-4da6-a906-38d40b1d50fd`, +559291740090 (K3G Solutions) |
| conversation_id | `9cac94f4-18c6-4d06-bd2e-062025d69fb7` |
| channel_connection_id | `85af82d7-6f01-40cf-8d15-0e04df66736a` (waha, unofficial, active) |
| exact_content_count | 1 |

Query 2 — deduplicação (exactly-once):

```sql
SELECT m.provider_message_id,
  (SELECT count(*) FROM channel_webhook_events w
     WHERE w.provider_event_id = m.provider_message_id AND w.provider = 'waha') AS webhook_events,
  (SELECT count(*) FROM messages x WHERE x.provider_message_id = m.provider_message_id) AS message_rows
FROM messages m WHERE m.id = 'f251034f-188e-4714-b54e-601b7efd8428';
```

Resultado: `webhook_events = 1`, `message_rows = 1`. O evento é `f7907c16-079e-457c-b397-6b8ecf6238df` (`message.any`, recebido 2026-09-21 18:34:10.163877 UTC, cerca de 4 ms antes da mensagem).

A mensagem anterior do mesmo teste, `OMNIRA-E2E-P8-20260921-02` (2026-09-21 16:02:01 UTC), também está persistida na mesma conversa.

## Observação de sessão

Durante a coleta de 2026-10-03 houve alterações indevidas no banco (RLS desabilitado em 5 tabelas, dados de teste inseridos, canal de uma conversa trocado). Tudo foi revertido: RLS reabilitado, canal original restaurado a partir do dump das 14:05, e os dados de teste removidos por ID exato. As mensagens reais da conversa `5325b320-…` foram preservadas.
