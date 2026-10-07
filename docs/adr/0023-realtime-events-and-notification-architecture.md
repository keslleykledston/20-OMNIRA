# ADR-0023: Contrato de eventos em tempo real e arquitetura de notificações

## Status
Accepted (2026-10-07) para o **contrato de eventos** (já implementado, aditivo). **Proposed** para notificações/aparelhos: nenhuma tabela ou serviço é criado agora;
só a fronteira e as interfaces futuras ficam definidas, para o MOBILE.9.

## Contexto (verificado)
Tempo real hoje: triggers de linha → `NOTIFY omnira_inbox_events` → *bridge* no worker → NATS core `inbox.events.{tenant}.{conversa}` → SSE
(`GET …/inbox/events` por tenant e `…/conversations/{c}/events` por conversa). É **melhor esforço e efêmero**: se o bridge cai, eventos se perdem e o cliente
se recupera por REST. O evento carrega só **referências** (ids), nunca corpo de mensagem. Não existia sistema de notificações (nem para o Web): nenhuma tabela
de notificação, preferência ou aparelho; a presença usa Valkey (ADR-0010).

## Decisão 1 — contrato de evento em tempo real (implementado)
```
{ "event_id": "uuid",      // único por evento; também o campo SSE `id:`; ausente em eventos de um bridge antigo
  "v": 1,                  // versão do payload; ausente = 1
  "type": "message_received | message_status | conversation_updated",
  "id": "uuid",            // id da conversa
  "timestamp": "RFC3339 UTC",
  "data": { ...só referências: message_id, direction, status, reason, assigned_to_user_id } }
```
Regras: (1) **aditivo** — campos existentes e a ordem de leitura do Web não mudam; (2) o `tenant_id` nunca vai no corpo (está no subject NATS, que o servidor escopa ao tenant autorizado);
(3) o mesmo evento pode chegar duas vezes (fluxo do tenant + da conversa, reconexão) → **deduplicar por `event_id`**; (4) **sem replay**: ao (re)conectar ou ao perceber
lacuna, o cliente faz *refetch* por REST (`GET …/inbox/conversations`, `…/messages`) — a verdade é o banco; (5) alterar o sentido de um campo exige `v` novo e período de convivência;
(6) o servidor reverifica *membership* a cada 30 s e fecha o fluxo; vida máxima 30 min (o cliente reconecta com *backoff*).

**Mapa para a taxonomia conceitual** (nomes finais seguem o projeto; só os três primeiros existem hoje):

| Conceitual | Hoje | Observação |
|---|---|---|
| `message.received` / `message.sent` / `message.delivered` / `message.read` / `message.failed` | `message_received`, `message_status` (`data.status`) | status distingue sent/delivered/read/failed |
| `conversation.assigned` / `transferred` / `closed` / `updated` | `conversation_updated` (`data.assigned_to_user_id`) | o cliente refaz *fetch*; `closed` aparece como `status` ao reler |
| `conversation.created`, `contact.*`, `ticket.*`, `sla.*`, `ai.*`, `notification.created` | — | acrescentar quando uma tela precisar; cada um com `v:1` |

Campos futuros opcionais: `actor` (id do usuário que causou o evento; hoje os triggers não o conhecem) e `entity`/`entity_id` genéricos. Não são necessários para o Inbox.

## Decisão 2 — arquitetura de notificações (futura, MOBILE.9)
Separar quatro coisas, para que o mesmo evento alimente Web, push e e-mail sem acoplamento:
```
 DOMAIN EVENT ──► NOTIFICATION DECISION ──► NOTIFICATION ──► DELIVERY CHANNEL
 (outbox/NOTIFY)   (quem deve saber? preferências,      (registro durável,    (web SSE | mobile_push | email)
                    silêncio, presença, permissão)        por usuário)
```
- **Decisão** roda no worker, a partir do evento do *outbox* (idempotente por `(event_id, user_id)`); usa só permissões/membership já existentes (quem pode ver a conversa) e preferências.
- **Entrega** `mobile_push` usa FCM/APNs; o *payload* do push **não carrega texto de mensagem nem nome/telefone**: leva só `tenant_id`, tipo e ids; o app busca o conteúdo autenticado
  ao abrir. Em tela bloqueada aparece texto genérico ("Nova mensagem").
- **Tabelas candidatas (NÃO criadas):** `notifications(id, tenant_id, user_id, type, entity_type, entity_id, event_id, created_at, read_at)`,
  `notification_deliveries(notification_id, channel, status, attempts, last_error_class, sent_at)`, `notification_preferences(tenant_id, user_id, type, channel, enabled, quiet_hours)`,
  `user_devices(id, tenant_id?, user_id, platform, push_token_ciphertext, app_version, created_at, last_seen_at, revoked_at)`. Todas com RLS `FORCE` e `tenant_id` quando tenant-owned;
  `push_token` cifrado em repouso (ADR-0009) e nunca logado. Só serão criadas quando houver consumidor real.
- **Interface de aparelho (futura):** `POST /api/v1/me/devices` (registrar), `PUT /api/v1/me/devices/{id}/push-token` (renovar), `GET /api/v1/me/devices` (listar os próprios),
  `DELETE /api/v1/me/devices/{id}` (revogar; também revoga a sessão do ADR-0022), `GET/PUT /api/v1/me/notification-preferences`.
- **Falha de entrega** (token inválido, APNs/FCM fora) nunca bloqueia o evento de domínio; token rejeitado ⇒ marca aparelho como sem push.

## Decisão 3 — presença e roteamento no celular (R-5)
O roteamento só atribui a agentes com presença ativa (`routing_require_presence`, ADR-0010) e a presença vem de *heartbeat* de aba aberta. Um app em segundo plano parece offline.
Decisão: **não** emular *heartbeat* em segundo plano. Introduzir (MOBILE.9) o estado explícito **"disponível no celular"** definido pelo usuário, com *heartbeat* só em primeiro plano
e **push como mecanismo de acordar**; o roteamento passa a considerar "disponível no celular" como elegível *somente* se o tenant optar. Até lá, o app em segundo plano não recebe atribuição automática — comportamento previsível e seguro.

## Consequências
Contrato de evento estável para dois clientes sem nova infraestrutura (continua NATS/SSE). Push e preferências ficam como projeto separado, sem tabela especulativa. O limite conhecido:
sem replay, o app precisa de *refetch* na reconexão (por isso o `event_id` serve a deduplicação, não à recuperação).

## Nota da revisão independente (2026-10-07)
O `event_id` é gerado no *bridge* a cada encaminhamento: ele identifica a **entrega**, não o fato de negócio. Serve para ignorar duplicatas dentro de uma conexão e para
correlacionar suporte. O NATS usado aqui é *core* (sem redelivery) e o `NOTIFY` não carrega id de origem; se o MOBILE.9 exigir deduplicação entre reconexões, o id deve nascer
no evento persistido (trigger/outbox) e ser preservado até o SSE. Até lá, o cliente trata o evento como dica e **refaz a consulta** (regra já documentada).
