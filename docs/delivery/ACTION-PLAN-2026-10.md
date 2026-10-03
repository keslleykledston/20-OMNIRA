# Plano de Ação — Outubro 2026

**Objetivo:** Roadmap para FIRST_REAL_INTERNAL_PRODUCT_DELIVERY + Piloto Supervisionado + Pre-Production

**Timeline:** Semanas de 2026-10-03 até 2026-10-31

**Owner:** Engineering (Claude Code + Team)

---

## 📊 Fase Atual: REAL_PRODUCT_VALIDATION_MODE

**Status:** P8 (Database Evidence) pendente execução  
**Bloqueador:** Operador autorizado com acesso ao banco remoto

---

## 🔴 SEMANA 1 (2026-10-03 a 2026-10-09)

### Prioridade 1: P8 — Coleta de Evidência de Banco

**Responsável:** Operador autorizado  
**Duração:** 10-15 min  
**Status:** ⏳ WAITING  

**Ações:**
1. Operador executa `docs/ops/P8-RUNBOOK-DATABASE-EVIDENCE.md`
   - QUERY 1: Localizar mensagem `OMNIRA-E2E-P8-20260921-03`
   - QUERY 2: Validar dedupe (exactly-once)
   - QUERY 3: Correlação webhook ↔ message
2. Reportar resultados (UUIDs + números, sem credenciais)
3. Marcar P8 = DONE

**Bloqueadores para continuar:** Nenhum; R2-R6 podem rodar em paralelo

---

### Prioridade 2: OIDC Produção (D-3) — Segurança

**Responsável:** Claude Code + DevOps  
**Duração:** 4-6h  
**Status:** 🟡 NOT STARTED  

**Ações:**
1. Configurar Keycloak em produção (servidor separado ou containerizado)
   - Realm: `omnira`
   - Users: Migrar de mock-auth para identidades reais
   - Certificado SSL: Válido e assinado

2. Atualizar `.env` em produção:
   ```bash
   OMNIRA_AUTH_MODE=oidc
   OMNIRA_AUTH_ISSUER=https://auth.devops.k3gsolutions.com.br/realms/omnira
   OMNIRA_AUTH_CLIENT_ID=omnira-web
   OMNIRA_AUTH_CLIENT_SECRET=[novo secret]
   OMNIRA_AUTH_REDIRECT_URL=https://omnira.devops.k3gsolutions.com.br/api/v1/auth/oidc/callback
   OMNIRA_AUTH_COOKIE_SECURE=true
   ```

3. Testar fluxo OIDC:
   - Login via Keycloak
   - JWT validation
   - Session persistence
   - Logout + revoke

4. Validar com `scripts/cleanroom-compose.sh` (mock + OIDC)

**Testes esperados:**
- ✓ Login mock (antes da mudança)
- ✓ Login OIDC (após)
- ✓ Token refresh
- ✓ Session timeout

**Bloqueador:** Nenhum; pode rodar em paralelo com P8/R2-R6

---

### Prioridade 3: Rate Limiting (D-2) — Segurança

**Responsável:** Claude Code  
**Duração:** 2-3h  
**Status:** 🟡 NOT STARTED  

**Ações:**
1. Implementar rate-limit middleware em `internal/platform/middleware/ratelimit.go`
   - Algorithm: Token bucket (Leaky Bucket)
   - Limits:
     - `/api/v1/auth/*`: 10 req/min per IP
     - `/api/v1/webhook/*`: 1000 req/min per channel_connection
     - `/api/v1/*`: 100 req/min per user

2. Integrar middleware na API:
   ```go
   // apps/api/cmd/omnira-api/main.go
   router.Use(ratelimit.Middleware())
   ```

3. Testes:
   - ✓ Rate limit exceeded → 429 Too Many Requests
   - ✓ Headers: `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`
   - ✓ Webhook rejeita flood (protege contra DoS do WAHA)

4. Não bloquear nada real (tunning em PILOT.4)

**Bloqueador:** Nenhum; desejável antes do piloto

---

## 🟡 SEMANA 2 (2026-10-10 a 2026-10-16)

### Prioridade 1: Gates R2-R6 — Validação de Produção

**Responsável:** Operador + Agents (2+)  
**Duração:** 50 minutos + análise  
**Status:** 🟡 READY TO RUN  

**Ações:**
1. Execute `docs/ops/GATES-SEQUENCE-AFTER-P8.md`
   - **R2 (Inbound):** Enviar msg real via WhatsApp → Validar no Inbox
   - **R3 (Assignment):** 2 agents → atribuição atômica + realtime
   - **R4 (Outbound):** Agent responde → msg chega no telefone cliente
   - **R6 (RLS):** Validar isolamento entre tenants

2. Compile relatórios por gate (template fornecido)

3. Análise:
   - Qualquer P0 bug → FIX imediato
   - Warnings → Documente como known issue
   - PASS em todos → Marque FIRST_REAL_INTERNAL_PRODUCT_DELIVERY

**Esperado:** Todos 4 gates PASS

---

### Prioridade 2: FR3 — Frontend Contacts + Contact 360

**Responsável:** Claude Code (ou Lovable para UI)  
**Duração:** 8-12h  
**Status:** 🟡 DESIGN READY  

**Ações:**
1. Checkout `feat/frontend-ios-rebuild`

2. Ler design spec: `docs/reference-kits/omnira-ui-design/references/contacts.md`

3. Reconstruir componentes:
   - `web/src/features/contacts/components/ContactList.tsx`
   - `web/src/features/contacts/components/ContactCard.tsx`
   - `web/src/features/contacts/components/Contact360View.tsx`
   - `web/src/features/contacts/types.ts`

4. Gate visual (antes de commitar):
   - Build: `npm run build`
   - Preview: `vite preview`
   - Screenshot: 1440×1024 vs. design
   - Validar: aria-current, computed styles, responsive

5. Commit quando gate visual aprovado

**Teste:**
- ✓ E2E: `scripts/e2e-inbox.sh` (não quebra tests existentes)
- ✓ Web unit: `npx vitest run`

**Bloqueador:** Nenhum; pode rodar em paralelo

---

### Prioridade 3: PILOT.4A — Health Checks + Alerting Local

**Responsável:** Claude Code  
**Duração:** 2-3h  
**Status:** 🟡 SCRIPT EXISTS  

**Ações:**
1. Validar health checks:
   - `/health/live` (readiness)
   - `/health/ready` (liveness)
   - NATS JetStream status: `scripts/nats-jetstream-check.sh`
   - WAHA status: `scripts/waha-health-check.sh`

2. Alerting local (no stdout):
   - NATS JetStream bytes: `<70%` OK, `70-90%` WARN, `>=90%` CRITICAL
   - WAHA connection: active/inactive + last_heartbeat
   - Outbox queue depth: `>1000` WARN, `>10000` CRITICAL

3. Cron job ou systemd timer (não ativado ainda):
   ```bash
   # scripts/run-check-with-alert.sh (já existe)
   # Chamar a cada 5 min durante piloto
   */5 * * * * /home/omnira/scripts/run-check-with-alert.sh
   ```

**Teste:** `scripts/test-notify-wrapper.sh` (já existe, sem envio real)

---

## 🟢 SEMANA 3-4 (2026-10-17 a 2026-10-31)

### Prioridade 1: PILOT.4 — Piloto Supervisionado

**Responsável:** Operador + Supervisor (24h observado)  
**Duração:** 24-48h  
**Status:** 🟡 READY AFTER GATES  

**Ações:**
1. Após GATES PASS, iniciar piloto supervisionado
   - Operação: 24h ininterrupta
   - Supervisor: Monitor health checks a cada 30 min
   - Alerts: Canal dedicado (Slack/email/ntfy)
   - Escalation: P0 →Engenharia imediato

2. Validações durante piloto:
   - [ ] Uptime: ≥99% (máx 14 min downtime)
   - [ ] Inbound: Sem perda de mensagem
   - [ ] Outbound: Sem perda de mensagem
   - [ ] RLS: Sem isolamento breach
   - [ ] Logs: Sem P0 errors
   - [ ] Database: Sem queries lentas

3. Ao final:
   - Documento: `docs/delivery/PILOT-4-REPORT.md`
   - Decisão: GO/NO-GO para pre-production hardening

---

### Prioridade 2: Media SSRF (D-4) — Segurança

**Responsável:** Claude Code  
**Duração:** 2-3h  
**Status:** 🟡 NOT STARTED  

**Ações:**
1. Implementar validação de downloads em `internal/worker/media/media.go`:
   - Allowlist de domínios WAHA
   - Validação de Content-Type
   - Tamanho máximo (ex: 100MB)
   - Timeout de conexão

2. Proxy local (opcional):
   - Cache de mídia em Valkey
   - Resereve para futuras análises

3. Testes:
   - ✓ Download válido: passa
   - ✓ SSRF attempt: rejeitado
   - ✓ Tamanho grande: truncado ou rejeitado

**Ativação:** Após piloto, antes de SLA-ready

---

### Prioridade 3: Outbox Retention (D-5) — Escalabilidade

**Responsável:** Claude Code  
**Duração:** 3-4h  
**Status:** 🟡 NOT STARTED  

**Ações:**
1. Criar migration para `outbox_archive` table:
   ```sql
   CREATE TABLE outbox_archive (
     id UUID PRIMARY KEY,
     status VARCHAR(32),
     created_at TIMESTAMPTZ,
     archived_at TIMESTAMPTZ DEFAULT NOW()
   );
   ```

2. Cleanup job (worker):
   ```go
   // apps/worker/cmd/omnira-worker/main.go
   // A cada 1h: Move mensagens com status='delivered' + created_at < 7d para archive
   // Soft-delete (is_archived=true) para auditoria
   ```

3. Testes:
   - ✓ Cleanup não afeta outbox_active
   - ✓ Archive cresce: expected
   - ✓ Queries em outbox_active: otimizadas

**Ativação:** Post-piloto, antes de SLA-ready

---

## 🏁 Critério de Conclusão — ROADMAP COMPLETO

### ✅ FIRST_REAL_INTERNAL_PRODUCT_DELIVERY

Requer:
- [x] P8 = DONE (database evidence)
- [x] R2-R6 = ALL PASS (gates)
- [x] Documentação atualizada
- [x] Zero P0 bugs

### ✅ PILOT.4 COMPLETED

Requer:
- [x] 24h supervisionado = DONE
- [x] Uptime ≥99% validado
- [x] Health checks + alerting local = DONE
- [x] Relatório final + GO/NO-GO decision

### ✅ PRE-PRODUCTION HARDENING (opcional antes SLA-ready)

Requer:
- [x] OIDC Produção = DONE
- [x] Rate Limiting = DONE
- [x] Media SSRF = DONE
- [x] Outbox Retention = DONE

### ✅ PRODUCTION SLA-READY

Requer:
- [x] Tudo acima ✅
- [x] IdP (Keycloak) operacional
- [x] TLS/edge configurado
- [x] Observabilidade validada (APM, alerting, logs)
- [x] Runbooks de operação
- [x] Backup/restore testado
- [x] Comunicação com stakeholders

---

## 📅 Timeline Resumida

| Data | Evento | Status |
|------|--------|--------|
| 2026-10-03 | P8 evidence runbook pronto | ✅ DONE |
| 2026-10-03 | Gates R2-R6 runbook pronto | ✅ DONE |
| **2026-10-03–04** | **P8 execução** | ⏳ BLOCKED (operador) |
| **2026-10-05–06** | **Gates R2-R6** | 🟡 READY AFTER P8 |
| **2026-10-07–09** | **OIDC + Rate Limit + FR3** | 🟡 READY IN PARALLEL |
| **2026-10-10–16** | **PILOT.4 (24h)** | 🟡 AFTER GATES PASS |
| **2026-10-17–23** | **Pre-prod hardening** | 🟡 AFTER PILOT PASS |
| **2026-10-24–31** | **Documentation + GO/NO-GO** | 🟡 FINAL REVIEW |

---

## 🔗 Referências

- **P8 Runbook:** `docs/ops/P8-RUNBOOK-DATABASE-EVIDENCE.md`
- **Gates Runbook:** `docs/ops/GATES-SEQUENCE-AFTER-P8.md`
- **Production Readiness:** `docs/delivery/PRODUCTION-READINESS-STATUS.md`
- **Frontend:** `docs/reference-kits/omnira-ui-design/`
- **Operações:** `docs/ops/RUNBOOK-INBOX-WAHA.md`

---

**Versão:** 1.0  
**Data:** 2026-10-03  
**Owner:** Engineering (Claude Haiku)  
**Próxima revisão:** 2026-10-10 (após P8 + GATES)

