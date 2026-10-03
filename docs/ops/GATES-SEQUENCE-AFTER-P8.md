# Gates R2-R6 — Sequência de Validação Após P8

**Objetivo:** Validar funcionalidades de produção em ordem antes de declarar FIRST_REAL_INTERNAL_PRODUCT_DELIVERY.

**Pré-requisito:** ✅ P8 (Database Evidence) = DONE

**Timeline esperada:** ~50 minutos (sem R1 + R5)

---

## 📊 Roadmap Visual

```
P8 (Database Evidence)
  ↓ DONE ✓
R2 (Inbound Real) ————→ Enviar msg de telefone real → Validar persistência
  ↓ DONE ✓
R3 (Multiagent Assignment) ————→ 2 agents login → assign → realtime
  ↓ DONE ✓
R4 (Outbound Real) ————→ Agent responde → msg vai p/ telefone cliente
  ↓ DONE ✓
R6 (RLS Isolation) ————→ Validar isolamento entre tenants
  ↓ DONE ✓
✅ FIRST_REAL_INTERNAL_PRODUCT_DELIVERY (gate humano)

BLOCKED (postpone):
  R1 (Android Pairing) — requer Android + ADB
  R5 (CRM Real) — requer IXC credentials
```

---

## 🔴 R2 — Inbound Real (WhatsApp → OMNIRA)

**Duração:** 15 minutos  
**Responsável:** Operador + Agent(s)

### Setup

1. **Ter 2 telefones:**
   - **Telefone A (WAHA):** Conectado à WAHA em produção
   - **Telefone B (Cliente):** Telefone real qualquer

2. **Acessar OMNIRA:**
   ```
   https://omnira.devops.k3gsolutions.com.br/
   Login: test@omnira.local
   Password: (mock-auth, sem senha)
   ```

### Execução

1. **Abra o Channels UI** (sidebar → Canais)
2. **Crie connection WAHA** (se não existir)
   - Clique "Create connection"
   - Faça scan do QR code com Telefone A (WhatsApp)
   - Aguarde status mudar para "active"
   - Nota: número do Telefone A (ex: +5592991881234)

3. **Do Telefone B**, envie mensagem para o número do Telefone A:
   ```
   Olá, teste R2 — 2026-10-03T14:30:00Z
   ```

4. **Verifique no Inbox:**
   - Abra Inbox UI (sidebar → Inbox)
   - Nova conversa deve aparecer em 2-5 segundos
   - Clique para abrir
   - Mensagem deve ser visível: "Olá, teste R2 —..."

### Validação (Checklist)

- [ ] Mensagem chegou no Inbox?
- [ ] Conteúdo exato é o esperado?
- [ ] Status da mensagem é "received"?
- [ ] Contact criado com número correto?
- [ ] Conversation associada ao channel_connection WAHA?

### Resultado

**✅ PASS:** Mensagem visível no Inbox  
**❌ FAIL:** Mensagem não apareceu em 5 segundos

**Se FAIL:**
- Verificar webhook: API logs (`docker compose logs api | grep webhook`)
- Verificar NATS: realtime bridge (`docker compose logs worker | grep realtime`)
- Re-enviar do Telefone B (WAHA pode estar desconectado)

---

## 🔵 R3 — Multiagent Assignment

**Duração:** 10 minutos  
**Responsável:** 2 Agents

### Setup

1. **Agent A:**
   ```
   Login: test@omnira.local
   ```

2. **Agent B:**
   ```
   Login: test@omnira.local (nova sessão/aba)
   ```

### Execução

1. **Ambos acessam Inbox** (mesma conversa R2)

2. **Agent A clica "Assign to Me"**
   - Status deve mudar para "assigned to Agent A"
   - Agent B deve ver a atualização em tempo real (2-3 segundos)

3. **Agent A responde:**
   ```
   Olá! Estamos aqui. Como posso ajudar?
   ```
   - Clique "Send"
   - Mensagem vai para Outbox

### Validação (Checklist)

- [ ] Agent A consegue clicar "Assign to Me"?
- [ ] Agent B vê a atribuição em tempo real?
- [ ] Nenhuma race condition (ambos não atribuem ao mesmo tempo)?
- [ ] Status da conversa muda para "assigned"?
- [ ] Resposta aparece em Outbox?

### Resultado

**✅ PASS:** Assignment atômico, realtime funciona  
**❌ FAIL:** Race condition OU realtime não funciona

**Se FAIL:**
- Verificar worker realtime: `docker compose logs worker | grep "assignment\|realtime"`
- Verificar NATS: `docker compose logs nats`

---

## 🟢 R4 — Outbound Real (OMNIRA → WhatsApp)

**Duração:** 15 minutos  
**Responsável:** Agent + Telefone real

### Setup

Continuar com R3 (mesma conversa, Agent A atribuído).

### Execução

1. **Agent A está na conversa** (R3)

2. **Agent A digita resposta:**
   ```
   Teste R4 — Mensagem outbound
   ```

3. **Clique "Send"**
   - Mensagem aparece em Outbox da UI
   - Status: `queued`

4. **Aguarde 5-10 segundos**
   - Status muda: `queued → sent → delivered`

5. **No Telefone B (Cliente), receba a mensagem**
   - "Teste R4 — Mensagem outbound" deve chegar
   - Timestamp deve ser próximo do envio

### Validação (Checklist)

- [ ] Mensagem entra em Outbox?
- [ ] Status muda de `queued` para `sent`?
- [ ] Mensagem chega no telefone cliente?
- [ ] Conteúdo exato é o esperado?
- [ ] Timestamp é próximo?

### Resultado

**✅ PASS:** Mensagem enviada e recebida  
**❌ FAIL:** Status fica `queued` ou mensagem não chega

**Se FAIL:**
- Verificar worker delivery: `docker compose logs worker | grep "delivery\|outbound"`
- Verificar WAHA: `docker compose logs waha`
- Verificar NATS: `docker compose logs nats`
- Verificar se connection WAHA está "active"

---

## 🟣 R6 — RLS Isolation

**Duração:** 10 minutos  
**Responsável:** Operador + 2+ Tenants

### Setup

1. **Criar 2 Tenants** (se não existir):
   - Tenant A (já existe via seed)
   - Tenant B (criar via API ou seed)

2. **Agents:**
   - Agent A1: membro de Tenant A
   - Agent B1: membro de Tenant B

3. **Dados:**
   - Conversa X: propriedade de Tenant A
   - Conversa Y: propriedade de Tenant B

### Execução

1. **Agent A1 login** → Vê apenas Tenant A
   - Inbox deve mostrar conversas de Tenant A
   - Não deve ver conversas de Tenant B

2. **Agent B1 login** → Vê apenas Tenant B
   - Inbox deve mostrar conversas de Tenant B
   - Não deve ver conversas de Tenant A

3. **Validar acesso cruzado:**
   ```bash
   # No banco, Agent A1 tenta ler conversa de Tenant B
   # Via API: GET /api/v1/conversations/<Y>/messages
   # Resultado esperado: 403 Forbidden ou lista vazia
   ```

### Validação (Checklist)

- [ ] Agent A1 não vê conversas de Tenant B?
- [ ] Agent B1 não vê conversas de Tenant A?
- [ ] API rejeita acesso cruzado (403)?
- [ ] RLS está ativo (`OMNIRA_BYPASS_RLS=false`)?
- [ ] Queries respeitam row-level security?

### Resultado

**✅ PASS:** Isolamento total entre tenants  
**❌ FAIL:** Qualquer acesso cruzado visível

**Se FAIL:**
- Verificar RLS policies: `\d+ messages` (no psql)
- Verificar role de aplicação: `SELECT current_user;` (no psql, como omnira_app)
- Verificar JWT tenant claim: `Authorization: Bearer <token>`

---

## 📋 Critério de Aprovação — FIRST_REAL_INTERNAL_PRODUCT_DELIVERY

### ✅ PASS Requisitos

- [x] R2 (Inbound): Mensagem recebida e persistida
- [x] R3 (Assignment): Atribuição atômica, realtime funciona
- [x] R4 (Outbound): Mensagem enviada para telefone real
- [x] R6 (RLS): Isolamento total entre tenants
- [x] P0 Bugs: 0 (sem crash, auth failure, RLS bypass)
- [x] Documentação: Atualizada
- [x] Evidência: Relatórios com logs/screenshots

### ❌ FAIL Requisitos

- [x] Qualquer P0 bug não resolvido
- [x] RLS violation detectado
- [x] Inbound/outbound message perda
- [x] Assignment race condition
- [x] Auth bypass

---

## 🚀 Próximos Passos Após R2-R6 PASS

### Fase 3A: Piloto Supervisionado (PILOT.4)

- [ ] **PILOT.4A:** Health checks + alerting local
- [ ] **PILOT.4B:** Operação supervisionada 24h
- [ ] **PILOT.4C:** WAHA session visibility + alerting
- [ ] **PILOT.4D:** JetStream retention + reconciliation
- [ ] **PILOT.4E:** Notificações externas ativadas

### Fase 3B: Pre-Production Hardening

- [ ] **D2 (Rate Limiting):** Implementar rate-limit
- [ ] **D3 (OIDC Produção):** Integrar Keycloak real
- [ ] **D4 (Media SSRF):** Validação de downloads
- [ ] **D5 (Outbox Retention):** Cleanup policy

---

## 📝 Template de Relatório por Gate

```
=====================================================
GATE R2 — INBOUND REAL — REPORT
=====================================================

Data: 2026-10-03
Hora: 14:30-14:45 UTC
Responsável: [Operador Name]

SETUP:
✓ Telefone A (WAHA): +5592991881234
✓ Telefone B (Cliente): +559291234567
✓ OMNIRA: https://omnira.devops.k3gsolutions.com.br

EXECUÇÃO:
✓ Mensagem enviada: "Olá, teste R2 — ..."
✓ Mensagem recebida no Inbox: SIM
✓ Timestamp no Inbox: 2026-10-03 14:31:05 UTC
✓ Status: received
✓ Contact criado: contact_id = 550e8400-...
✓ Conversation criada: conversation_id = 660f9411-...

VALIDAÇÃO:
✓ Conteúdo exato? SIM
✓ Provider = waha? SIM
✓ Direction = inbound? SIM
✓ RLS isolamento? SIM (Tenant A isolado)

RESULTADO: ✅ PASS

Logs relevantes:
- API webhook: [link para log]
- Worker realtime: [link para log]

Notas: [qualquer detalhe relevante]

=====================================================
```

---

## 🔗 Referências

- **P8 Runbook:** `docs/ops/P8-RUNBOOK-DATABASE-EVIDENCE.md`
- **Architecture:** `docs/architecture/`
- **Production Readiness:** `docs/delivery/PRODUCTION-READINESS-STATUS.md`
- **Runbook WAHA:** `docs/ops/RUNBOOK-INBOX-WAHA.md`

---

**Versão:** 1.0  
**Data:** 2026-10-03  
**Owner:** Engineering  
**Status:** READY FOR EXECUTION

