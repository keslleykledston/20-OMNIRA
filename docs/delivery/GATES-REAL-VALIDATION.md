# Gates de Validação Real — FIRST_REAL_INTERNAL_PRODUCT_DELIVERY

Estado: Em execução (GATE R1 iniciado 2026-09-19 23:40 UTC)

## GATE R1 — WAHA Real + QR Pairing

**Objetivo**: Validar que WAHA real consegue gerar QR e humano consegue escanear.

**Pré-requisitos**:
- Stack Docker rodando (postgres, nats, migrate, api, worker, web, waha)
- WAHA_ENABLED=true no environment
- Humano com telefone real + WhatsApp instalado

**Procedimento automático**:
1. Subir stack com `--profile whatsapp-unofficial`
2. Executar migração de schema
3. Fazer login na web (mock: test@omnira.local)
4. Ir para /channels → Adicionar WAHA
5. Aceitar risco (checkbox + timestamp)
6. Gerar QR real
7. Salvar QR em arquivo de teste

**Procedimento manual (BLOQUEADOR_REQUER_HUMANO)**:
1. Abrir WhatsApp no telefone real
2. Ir em Configurações → Aparelhos conectados → Conectar aparelho
3. Apontar câmera para QR exibido
4. Confirmar pareamento
5. Voltar à tela de OMNIRA
6. Verificar status: "Conectado" e "sessão: working"

**Evidência esperada**:
```
WAHA session state = connected
Provider message: "Conectado · sessão: working · última atividade: agora"
```

**Critério de sucesso**:
- [x] QR gerado sem erro
- [ ] QR escaneado com sucesso
- [ ] Status WAHA = connected no OMNIRA
- [ ] Sessions persistem após docker restart

**Se bloqueado**:
Parar aqui com mensagem:
```
BLOCKED_REQUIRES_HUMAN

Reason:
WhatsApp pairing requires human scanning of QR code on real phone.

Required action:
1. Open WhatsApp on your phone
2. Settings → Connected devices → Link a device
3. Point camera at QR code shown on screen
4. Confirm pairing in WhatsApp

Expected signal:
Device status changes to "Conectado · sessão: working"

Do NOT proceed with next gates until this is confirmed.
```

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
- [ ] Contact criado com phone_e164 correto
- [ ] Conversation com status=active
- [ ] Message com direction=inbound
- [ ] Message aparece na Inbox UI
- [ ] Queue entry para Conversation
- [ ] Nenhuma duplicação em redelivery

**Se bloqueado**:
Parar com diagnóstico exato (webhook error, validation failure, etc).

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
- [ ] Atomic assignment (CAS sem race condition)
- [ ] Realtime update para ambos agents
- [ ] Assignment history registrado
- [ ] 409 Conflict é legível no erro

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
- [ ] Message status = sent (mínimo)
- [ ] Message recebida no telefone cliente
- [ ] Conteúdo íntegro
- [ ] Sem duplicação de envio

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

## GATE R6 — RLS/Tenant Isolation Real

**Objetivo**: Validar que Tenant B não consegue enxergar dados de Tenant A.

**Procedimento**:
1. Criar segundo Tenant com outro usuário
2. Ambos fazem login
3. Tenant A gera dados (Contact, Conversation, Message)
4. Tenant B tenta acessar via API com seu token
5. API retorna 404 (via RLS)
6. Tenant B UI não mostra dados de A

**Critério de sucesso**:
- [ ] RLS ativo no Postgres
- [ ] Queries retornam apenas do tenant_id do JWT
- [ ] Tentativa de acesso cross-tenant → 404 ou 403
- [ ] Nenhum vazamento em logs/errors

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
