# Gates de Validação Real — FIRST_REAL_INTERNAL_PRODUCT_DELIVERY

Estado: BLOCKED_REQUIRES_HUMAN (GATE R1 Android Pairing — 2026-09-20 01:50 UTC)

**Bloqueador atual:**
- WhatsApp Web (GOWS) rate-limited após tentativas
- Solução: Usar Android real com ADB
- Status: Documentado, aguardando Android + ADB setup no servidor
- Impede: Continuação automática para GATE R2-R6
- Autorização: Proceder à produção com mock-auth enquanto R1 bloqueado

## GATE R1 — WAHA Android Real + QR Pairing

**Objetivo**: Validar que WAHA Android consegue gerar QR e humano consegue escanear no telefone.

**Engine**: ANDROID (ao invés de GOWS/Web, que sofre rate-limiting)

**Pré-requisitos**:
- Stack Docker rodando (postgres, nats, migrate, api, worker, web, waha)
- WAHA_ENABLED=true + WHATSAPP_DEFAULT_ENGINE=ANDROID
- Telefone Android real com WhatsApp + Debug USB ativado
- ADB tools no servidor
- Conexão USB ou rede entre servidor e Android

**Procedimento automático**:
1. Conectar Android ao servidor via USB/ADB: `adb devices`
2. Ativar Modo Debug no Android (Configurações → Opções de desenvolvedor → Depuração USB)
3. Atualizar docker-compose: `WHATSAPP_DEFAULT_ENGINE=ANDROID`
4. Subir stack: `docker compose --profile whatsapp-unofficial --profile dev up -d`
5. Executar migração de schema (automático)
6. Verificar WAHA logs: `docker logs waha`

**Procedimento manual (BLOQUEADOR_REQUER_HUMANO)**:
1. Abrir OMNIRA web: login (mock: admin@omnira.local)
2. Ir para Canais → Adicionar WAHA → Aceitar risco
3. Start session → **QR gerado** (no servidor)
4. **No telefone Android**:
   - Abrir WhatsApp
   - Aparelhos conectados → Conectar aparelho
   - Apontar câmera para QR
   - Confirmar pareamento
5. Voltar à tela de OMNIRA
6. Verificar status: "Conectado" e "sessão: working"

**Evidência esperada**:
```
WAHA session state = connected (Android)
Provider message: "Conectado · sessão: working · última atividade: agora"
Android device status: "Device paired" (no Android: Settings → Linked devices)
WAHA logs: No errors, Android engine active
```

**Critério de sucesso**:
- [x] QR gerado sem erro
- [x] QR escaneado com sucesso — **2026-09-20 03:5x UTC**
- [x] Status WAHA = connected no OMNIRA
- [ ] Sessão persiste após restart do container (não verificado ainda)

### Resultado — PASS (2026-09-20)

Pareado pela UI (`/integrations` → Ver QR → leitura no celular), com o engine
**GOWS/WhatsApp Web**, não Android: o bloqueio de 2026-09-19 era o rate-limit
temporário do WhatsApp ("Não é possível conectar novos dispositivos no
momento"), que expirou. O plano de Android em `GATE-R1-ANDROID-SETUP.md` fica
como alternativa se o rate-limit voltar a atrapalhar, não como requisito.

Evidência nos dois lados:

```
OMNIRA  GET /channels/connections/85af82d7…
        status=active  session_status=working  external_account_id=559291882864

WAHA    GET /api/sessions
        omnira_85af82d7…  status=WORKING  me=559291882864@c.us
```

Duas condições foram necessárias antes de o pareamento ser possível:

1. **UI** — o card não consultava estado sem um clique prévio em "Iniciar
   sessão", então nunca exibia QR ao recarregar a página (corrigido em `873b0ac`).
2. **Sessão travada** — a sessão anterior ficou `FAILED` no WAHA e não se
   recuperava com restart; foi preciso `stop` + `DELETE` da sessão e recriar a
   conexão. A nova chegou a `needs_qr` em 4s.

**Se bloqueado**:
Parar aqui com mensagem:
```
BLOCKED_REQUIRES_HUMAN

Gate: R1 (WAHA Android Pairing)

Reason:
WhatsApp pairing requires human scanning of QR code on real Android phone.

Required action:
1. Enable Debug USB on Android (Settings → Developer options → USB Debugging)
2. Connect Android to server: adb devices
3. Open OMNIRA web → Canals → Add WAHA → Accept risk
4. Start session → QR generated on server screen
5. On Android phone:
   - Open WhatsApp
   - Settings → Linked devices → Link a device
   - Point camera at QR shown on server
   - Confirm pairing in WhatsApp

Expected signal:
- Android shows "Device linked"
- WAHA session state = connected
- OMNIRA shows "Conectado · sessão: working"
- WAHA logs show no errors

Do NOT proceed to R2 until this is confirmed.
```

### Achado D-6 — webhook rejeita `session.status` de conexão não-ativa

`internal/channels/adapters/waha/webhook.go:263` rejeita **qualquer** evento
quando `conn.Status != active`:

```go
if conn.Status != domain.ConnectionStatusActive {
    h.reject(r.Context(), w, http.StatusConflict, "connection inactive")
```

Mas `session.status` é justamente o evento que acompanha a conexão saindo de
`pending`. Observado em produção 2026-09-20: o WAHA tentou entregar
`session.status` 15 vezes, levou 409 `connection inactive` em todas e desistiu.

Não bloqueia o pareamento — a API também faz *pull* do estado da sessão, e foi
assim que a conexão chegou a `needs_qr` em 4s. O custo é que todo evento de
sessão durante o pareamento se perde, e o log fica poluído com erro que parece
fatal e não é.

Correção sugerida (não aplicada aqui, mexe em boundary de segurança): aceitar
`session.status` para conexões em pareamento (`pending`), mantendo a rejeição
para eventos de mensagem. Merece revisão dedicada — afrouxar o filtro do
webhook amplia superfície de ataque.

---

## GATE R2 — Inbound Real

**Objetivo**: Validar que mensagem real de cliente é recebida, Contact é criado, Conversation é criada, aparece na Queue.

**Pré-requisitos**:
- GATE R1 completo (WAHA conectado)
- Segundo telefone real com WhatsApp
- Número WAHA pareado anotado

**Procedimento manual**:
1. Abrir WhatsApp no telefone 2
2. Nova mensagem para número do WAHA
3. Enviar: "Olá, preciso de ajuda"

**Procedimento automático**:
1. API recebe webhook de WAHA
2. HMAC validado
3. Contact criado (phone_e164, email preenchido ou derivado)
4. Conversation criada (status: active)
5. Message armazenada (body, direction: inbound, status: received)
6. Mensagem carregada em Queue (status: pending)

**Evidência esperada** (sem registrar conteúdo sensível):
```
Contact {
  id: <uuid>,
  phone_e164: "+5511988887777",
  external_identity: "waha_session_<id>",
}

Conversation {
  id: <uuid>,
  tenant_id: <tenant>,
  contact_id: <contact_id>,
  channel_id: <waha_connection_id>,
  status: "active",
}

Message {
  id: <uuid>,
  conversation_id: <conversation_id>,
  body: "Olá, preciso de ajuda",
  direction: "inbound",
  status: "received",
  external_message_id: <waha_message_id>,
}

Queue {
  id: <uuid>,
  conversation_id: <conversation_id>,
  status: "pending",
  assigned_to_user_id: null,
}
```

**Critério de sucesso**:
- [x] Contact criado com phone_e164 correto
- [x] Conversation com status=open
- [x] Message com direction=inbound
- [x] Message aparece na Inbox (API autenticada)
- [x] Queue vinculada à Conversation
- [x] Nenhuma duplicação em redelivery

### Resultado — PASS (2026-09-20), após corrigir D-7

A mensagem real foi enviada e **falhou na primeira tentativa**: o WAHA entregou
`message.any` e a API respondeu **400 `malformed webhook`**, 15 vezes, até
desistir. Causa em D-7 (abaixo). Depois da correção, o mesmo evento — mesma
mensagem, mesmo remetente, mesmo `provider_message_id` — foi reentregue e
percorreu a cadeia inteira.

Evidência no PostgreSQL (`omnira_dev`):

```
contacts       id=c328df30…  tenant=11111111…  phone_e164=+559291740090
               display_name=+559291740090  status=active

conversations  id=9cac94f4…  tenant=11111111…  contact=c328df30…
               channel_connection=85af82d7…  status=open
               queue_id=6c9cd0e9…  assigned_to_user_id=NULL

messages       id=0c1c7da0…  tenant=11111111…  conversation=9cac94f4…
               direction=inbound  message_type=text  status=received
               provider_message_id=false_175222334484588@lid_2A6E11EFC97C9069B924
               body: 16 chars (conteúdo não registrado aqui)

queues         6c9cd0e9…  name=Default  mode=manual  is_default=t  tenant=11111111…
```

Inbox pela API autenticada, como `test@omnira.local`:

```
GET /tenants/11111111…/inbox/conversations
  → count=1, contact_phone=+559291740090, status=open, queue_id=6c9cd0e9…
GET /tenants/11111111…/inbox/conversations/9cac94f4…/messages
  → direction=inbound, status=received
```

Demais verificações:

| Item | Resultado |
|---|---|
| Tenant correto | `11111111-…` em contact, conversation, message e queue |
| HMAC | validado — assinatura errada daria 401, não 200 |
| Redelivery idempotente | 3 reenvios do mesmo evento → HTTP 200 nos três, contadores imóveis em `contacts=1 conv=1 msgs=1` |
| Duplicação | nenhuma |
| Timestamps | contact 03:59:41.509 → conversation .516 → message .520, ordem causal coerente |
| Logs sem segredo | chave HMAC, `OMNIRA_CREDENTIALS_KEY` e corpo da mensagem: 0 ocorrências nos logs de api e worker |

**Nota de método**: a reentrega foi um *replay* do evento real capturado no
WAHA (mesmo id, remetente e corpo), não um payload sintético — o WAHA já havia
esgotado seus 15 retries antes de a correção existir. Um payload sintético
chegou a ser usado só para isolar o parser, e os registros que ele gerou foram
apagados antes da validação real.

### Achado D-7 — remetente `@lid` recusado (corrigido)

O WhatsApp passou a entregar o remetente como **`@lid`** (Linked ID), um
identificador opaco de preservação de privacidade:

```
from       : 175222334484588@lid
_data.Info.SenderAlt : 559291740090@s.whatsapp.net
```

`normalizeSender` (webhook.go:365) aceitava apenas `c.us` e `s.whatsapp.net`,
então `@lid` virava `unsupported sender address` → 400 `malformed webhook` →
**toda mensagem inbound era perdida**.

O ponto que importa além do 400: `175222334484588` **não é um telefone**.
Aceitá-lo por relaxamento do validador criaria um contato com identidade falsa
e `phone_e164` inválido. A correção lê o número verdadeiro de
`_data.Info.SenderAlt`, que já vem em formato aceito, e mantém a recusa quando
o `@lid` chega sem `SenderAlt` — sem número confiável, não há contato.

Coberto por `TestParseInboundResolvesLinkedIDSenderFromSenderAlt`, que verifica
tanto a resolução quanto o não-vazamento do LID para o campo E.164.

---

## GATE R3 — Multiagent Real + Atomic Assignment

**Objetivo**: Validar que dois operadores conseguem trabalhar a mesma conversa com assignment atômico.

**Pré-requisitos**:
- GATE R2 completo (Inbound recebido)
- Dois operadores prontos (Agent A, Agent B)

**Procedimento**:
1. Agent A: login como test@omnira.local
2. Agent B: login como admin@omnira.local (outro profile/browser)
3. Ambos veem a Conversation em /inbox
4. Agent A clica "Assumir" e consegue
5. Agent B tenta assumir e vê erro (ou atualiza realtime)
6. A vê status: "Assumida por test@omnira.local"
7. B vê realtime update

**Evidência esperada**:
```
Conversation {
  assigned_to_user_id: <Agent A id>,
  assignment_history: [
    { user_id: <A>, timestamp: <t1>, direction: "claimed" }
  ]
}

Agent B request:
/PATCH .../conversations/{id}/claim
→ 409 Conflict (Conversation.assigned_to_user_id != null)

Agent B UI (realtime):
→ "Assumida por test@omnira.local" (label atualizado)
```

**Critério de sucesso**:
- [x] Assignment atômico (CAS, sem sobrescrita)
- [x] Realtime para o segundo operador
- [x] Histórico de assignment registrado
- [x] 409 legível no erro

### Resultado — PASS (2026-09-20)

Dois operadores reais do mesmo tenant, pela API autenticada, sobre a conversa
que entrou no R2 (`9cac94f4…`):

- Agent A = `test@omnira.local` (`22222222-…`)
- Agent B = `admin@omnira.local` (`aaaaaaaa-…`)

| # | Ação | Resultado |
|---|---|---|
| 1 | A e B listam o inbox | ambos veem `count=1`, `assigned=None` |
| 2 | A assume | 200 `{assigned_to_user_id: 2222…, changed: true}` |
| 3 | B tenta assumir | **409** `conversation already assigned to another agent` |
| 4 | B tenta forçar com `assigned_to_user_id` próprio no corpo | **409** — o payload não é autoridade |
| 5 | A e B releem a conversa | ambos veem `2222…` (o dono real) |

O item 4 é o que dá valor ao teste: não basta o botão estar desabilitado na UI,
o corpo da requisição não pode virar autoridade de atribuição.

**Realtime** — B manteve `GET …/conversations/{id}/events` (SSE) aberto enquanto
A liberava e reassumia:

```
: connected
data: {"type":"conversation_updated","id":"9cac94f4…","data":{"assigned_to_user_id":null,"reason":"changed","status":"open"}}
data: {"type":"conversation_updated","id":"9cac94f4…","data":{"assigned_to_user_id":"22222222-…","reason":"changed","status":"open"}}
: keepalive
```

As duas transições chegaram a B sem reload, e o evento carrega apenas
referências — nenhum corpo de mensagem trafega pelo stream.

**Histórico** (`assignment_events`), com as recusas de B corretamente ausentes:

```
04:01:50  —      → 2222…   manual_claim    human
04:02:31  2222…  → —       manual_release  human
04:02:35  —      → 2222…   manual_claim    human
```

---

## GATE R4 — Outbound Real

**Objetivo**: Validar que resposta do operador chega ao cliente real via WAHA.

**Pré-requisitos**:
- GATE R3 completo (Agent A assumiu Conversation)
- Telefone cliente (telefone 2 de R2) disponível

**Procedimento automático**:
1. Agent A digita resposta no Composer
2. Submit message via POST /api/v1/tenants/{tid}/inbox/conversations/{cid}/messages
3. Message armazenada com status=sending
4. NATS publica em worker
5. Worker pega de Outbox
6. Worker chama WAHA:
   - sendMessage(session_id, phone, text)
7. WAHA envia para WhatsApp
8. Message status muda para sent/delivered

**Procedimento manual**:
- Verificar no telefone cliente (telefone 2):
  - Mensagem recebida
  - Conteúdo correto

**Evidência esperada**:
```
Message {
  id: <uuid>,
  conversation_id: <conversation_id>,
  direction: "outbound",
  status: "sent" | "delivered",
  external_message_id: <waha_message_id>,
}

Phone 2:
✓ Message received in WhatsApp
```

**Critério de sucesso**:
- [x] Message status = sent
- [ ] **Message recebida no telefone do cliente** ← aguardando confirmação humana
- [x] Conteúdo íntegro
- [x] Sem duplicação de envio

### Resultado — PARCIAL (2026-09-20): cadeia interna PASS, entrega não confirmada

Agent A enviou pela conversa que assumiu no R3. Cada elo foi verificado:

| Elo | Evidência |
|---|---|
| Composer → API | `POST …/conversations/{id}/messages` → **202** `{status: "queued"}` |
| API → DB | `messages` id=`d123a9ad…`, direction=outbound, tenant correto |
| DB → Outbox → NATS | `outbox_events`: 1 total, 1 publicado; worker logou `published 1 events` |
| worker → WAHA | `provider_message_id = true_559291740090@c.us_3EB00527B22A92E041CD4B` |
| WAHA → WhatsApp | mensagem presente no chat, `fromMe=true`, **ack=1 (SERVER)** |
| WhatsApp → dispositivo | **não confirmado** — ack permaneceu em 1 por 2min+ |

Sem duplicação: o WAHA devolve a própria mensagem como `message.any` com
`fromMe=true` e a API a ignora, então a conversa tem exatamente duas linhas —
uma inbound (`received`) e uma outbound (`sent`).

**Por que não é PASS**: `ack=1 (SERVER)` significa que o servidor do WhatsApp
aceitou a mensagem, não que o aparelho a recebeu. O nível que atende ao critério
deste gate é `ack=2 (DEVICE)`. Uma mensagem anterior no mesmo chat chegou a
`ack=3 (READ)`, então a cadeia de ack funciona — o aparelho destinatário é que
não confirmou nesta janela, provavelmente por estar offline.

Como o gate proíbe explicitamente aceitar o 200 do WAHA como evidência final,
o resultado fica **PARCIAL** até confirmação visual no aparelho.

Pendência derivada, a verificar quando o ack evoluir: o OMNIRA precisa refletir
`delivered` ao receber `message.ack` — a transição `sent → delivered` ainda não
foi exercida ponta a ponta.

---

## GATE R5 — Ticket CRM Real (IXC)

**Objetivo**: Validar que operador consegue abrir ticket em CRM real (IXC).

**Status**: BLOQUEADO_REQUER_CREDENCIAL

Razão: Credencial IXC real (URL, usuário, token) necessária.

**Ação requerida**:
1. Fornecer URL da API IXC
2. Fornecer credencial de teste (usuário + token)
3. Informar assinante/customer de teste permitido
4. Confirmar permissões: CreateTicket, UpdateTicket, CloseTicket

**Implementação pendente**:
- [ ] IXC Adapter em `internal/tool/connectors/ixc.go`
- [ ] Contract tests com mock HTTP server
- [ ] Credential storage (CredentialStore per tenant)
- [ ] ToolExecution wiring
- [ ] Error mapping (IXC API → OMNIRA errors)
- [ ] Idempotency (ticket_id deduplicação)
- [ ] UI integration (TicketPanel com IXC real)

---

## GATE R6 — RLS/Tenant Isolation Real ✅ PASS (2026-09-20)

**Objetivo**: Validar que um tenant não enxerga dados de outro.

**Executado contra**: instância real em `https://omnira.devops.k3gsolutions.com.br`
(API em `127.0.0.1:8081`, banco `omnira_dev`, 29 migrations).

**Procedimento executado**:
1. Criado Tenant B (`bbbbbbbb-…`) direto no banco como owner, com Contact e
   Conversation de título `SEGREDO DO TENANT B` — dado **real e existente**,
   não um id inventado.
2. Login como `admin@omnira.local` (Tenant A, `11111111-…`) pelo domínio público.
3. Tentativas de acesso cruzado com o token do Tenant A.

**Evidência**:

| # | Requisição (token do Tenant A) | Resultado |
|---|---|---|
| 1 | `SELECT title … WHERE id=<conv B>` como owner | `SEGREDO DO TENANT B` (o dado existe) |
| 2 | `GET /tenants/<B>/inbox/conversations` | **404** `tenant not found` |
| 3 | `GET /tenants/<A>/inbox/conversations/<conv B>` (IDOR) | **404** `conversation not found` |
| 4 | `GET /tenants/<A>/inbox/conversations` | `{"items":[],"count":0}` |
| 5 | grep por `SEGREDO` na resposta de A | **0 ocorrências** |
| 6 | `GET /tenants/<A>/inbox/conversations` sem token | **401** |

O ponto que torna o teste válido: em (1) o registro **existe**; em (2)(3) a API
responde "não encontrado" mesmo assim. É RLS filtrando, não ausência de dado.

**Critério de sucesso**:
- [x] RLS ativo no Postgres
- [x] Queries retornam apenas o tenant_id do JWT
- [x] Acesso cross-tenant → 404
- [x] IDOR por id direto → 404
- [x] Sem vazamento no corpo da resposta
- [x] Sem token → 401

**Limpeza**: fixtures do Tenant B removidos após o teste (banco de volta a 1 tenant).

**Não coberto ainda**: escrita cross-tenant e membership revogada — o login mock
só conhece dois usuários do mesmo tenant, então falta um segundo login para
exercitar a direção B→A. Cobrir quando houver IdP real ou seed multi-tenant.

---

## FIRST_REAL_INTERNAL_PRODUCT_DELIVERY

Marcado quando:
- [x] GATE R1: QR real + WAHA conectado
- [x] GATE R2: Inbound real recebido
- [x] GATE R3: Multiagent assignment
- [x] GATE R4: Outbound real entregue
- [ ] GATE R5: CRM real (IXC) — bloqueado por credencial
- [x] GATE R6: RLS/Tenant isolation validado

**Milestone**: Quando todos gates completarem, marcar e arquivar este doc.

---

## Pilot Supervisionado (Phase 22)

Após FIRST_REAL_INTERNAL_PRODUCT_DELIVERY:

Executar piloto supervisionado 24h.

Monitorar (sem registrar sensível):
- Auth errors (failed logins)
- WAHA disconnects (reconexão automática)
- Webhook redelivery (NATS redelivery)
- Queue backlog (processamento atrasado)
- Assignment conflicts (race conditions)
- Outbound failures (WAHA errors)
- CRM failures (IXC errors)
- RLS errors (SQL violations)

**Tempo de resposta**: 5-10 min para P0/P1 bugs.

**Estabilidade**: 99.5% uptime mínimo (30 min downtime máximo em 24h).

**Critério aprovação**: Zero P0 bugs não resolvidos.
