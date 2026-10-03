# P8 — Runbook de Coleta de Evidência de Banco de Dados

**Objetivo:** Validar que mensagem WAHA foi persistida corretamente no banco remoto de produção.

**Precondições:**
- Acesso SSH/VPN ao servidor de produção (`omnira.devops.k3gsolutions.com.br`)
- Credenciais do banco de dados de produção (read-only)
- PostgreSQL client (`psql`) ou ferramenta de SQL

**Duração esperada:** 10-15 minutos

**Risco:** Nenhum (queries read-only, sem alterações)

---

## 📍 PASSO 1: Conectar ao Banco Remoto

### Opção A: SSH + psql local

```bash
# 1. Acessar servidor de produção via SSH
ssh k3g-prod

# 2. De lá, conectar ao banco PostgreSQL
# Substitua <DB_HOST>, <DB_USER>, <DB_NAME> pelos valores de produção
psql -h <DB_HOST> -U <DB_USER> -d <DB_NAME>

# Exemplos:
# psql -h postgres.prod.internal -U omnira_app -d omnira_prod
# psql -h 10.0.1.100 -U omnira_app -d omnira_dev
```

### Opção B: Diretamente do host local (se VPN + firewall permitir)

```bash
# Via tunnel SSH ou conexão direta
psql -h <PROD_DB_IP_OR_HOSTNAME> \
     -U <DB_USER> \
     -d <DB_NAME> \
     -p 5432 \
     --set=sslmode=require
```

**Status esperado:**
```
omnira_prod=> 
```

Se conectar, prossiga para PASSO 2.

---

## 🔍 PASSO 2: Executar QUERY 1 (Localizar Mensagem)

**Propósito:** Encontrar a mensagem exata com todos os IDs correlacionados.

**Copie e cole no terminal `psql`:**

```sql
SELECT 
  m.id as message_id,
  m.provider_message_id,
  c.id as contact_id,
  c.phone_e164 as from_number,
  conv.id as conversation_id,
  conv.channel_connection_id,
  cc.provider,
  cc.provider_kind,
  m.direction,
  m.status,
  m.created_at,
  (SELECT COUNT(*) FROM messages WHERE body = 'OMNIRA-E2E-P8-20260921-03') as exact_content_count
FROM messages m
  JOIN conversations conv ON m.conversation_id = conv.id
  JOIN contacts c ON conv.contact_id = c.id
  JOIN channel_connections cc ON conv.channel_connection_id = cc.id
WHERE m.body = 'OMNIRA-E2E-P8-20260921-03'
  AND cc.provider = 'waha'
ORDER BY m.created_at DESC
LIMIT 10;
```

### Resultado Esperado

**✅ SUCESSO (1 linha):**
```
             message_id              |      provider_message_id       | contact_id | from_number | ... | exact_content_count
-------------------------------------+--------------------------------+------------+-------------+-----+-
 550e8400-e29b-41d4-a716-446655440000 | waha_msg_20260921_14_34_00001  | 123abc...  | +5592917... | ... | 1
```

**Copie os valores para o relatório:**
- `message_id` = 550e8400-e29b-41d4-a716-446655440000
- `provider_message_id` = waha_msg_20260921_14_34_00001
- `contact_id` = 123abc...
- `from_number` = +5592917...
- `conversation_id` = [valor da coluna]
- `channel_connection_id` = [valor da coluna]
- `provider` = waha
- `provider_kind` = unofficial
- `direction` = inbound
- `status` = received (ou similar)
- `created_at` = [timestamp exato]
- `exact_content_count` = 1

**❌ FALHA (0 linhas):**
```
(0 rows)
```
→ Significa: banco remoto diferente OU mensagem não foi persistida. Consulte o operador que configurou a produção.

**❌ FALHA (>1 linhas):**
```
(2 rows)  ou  (3 rows)
```
→ Significa: há **duplicatas** (não esperado). Consulte o time de engenharia antes de prosseguir.

---

## 🔐 PASSO 3: Executar QUERY 2 (Validar Dedupe)

**Propósito:** Confirmar que webhook foi processado UMA ÚNICA VEZ.

**Copie e cole no terminal `psql`:**

```sql
SELECT 
  m.provider_message_id,
  (SELECT COUNT(*) FROM channel_webhook_events 
   WHERE provider_event_id = m.provider_message_id 
   AND provider = 'waha') as webhook_events_received,
  (SELECT COUNT(*) FROM messages 
   WHERE provider_message_id = m.provider_message_id) as message_rows_persisted,
  CASE 
    WHEN (SELECT COUNT(*) FROM channel_webhook_events 
          WHERE provider_event_id = m.provider_message_id AND provider = 'waha') = 0 THEN 'WARNING: no webhook event'
    WHEN (SELECT COUNT(*) FROM channel_webhook_events 
          WHERE provider_event_id = m.provider_message_id AND provider = 'waha') > 1 THEN 'FAIL: duplicate events'
    WHEN (SELECT COUNT(*) FROM messages 
          WHERE provider_message_id = m.provider_message_id) > 1 THEN 'FAIL: duplicate message rows'
    WHEN (SELECT COUNT(*) FROM messages 
          WHERE provider_message_id = m.provider_message_id) = 0 THEN 'FAIL: no message row'
    ELSE 'PASS: exactly-once'
  END as dedupe_status
FROM messages m
WHERE m.body = 'OMNIRA-E2E-P8-20260921-03'
  AND m.provider_message_id <> '';
```

### Resultado Esperado

**✅ SUCESSO:**
```
      provider_message_id       | webhook_events_received | message_rows_persisted | dedupe_status
--------------------------------+------------------------+------------------------+---
 waha_msg_20260921_14_34_00001  | 1                      | 1                      | PASS: exactly-once
```

**Copie para o relatório:**
- `webhook_events_received` = 1
- `message_rows_persisted` = 1
- `dedupe_status` = PASS: exactly-once

**⚠️ AVISO (não é FAIL, mas investigar):**
```
| webhook_events_received | dedupe_status
|           0             | WARNING: no webhook event
```
→ Webhook não foi registrado; verifique se WAHA está enviando webhooks.

**❌ FALHA:**
```
| webhook_events_received | message_rows_persisted | dedupe_status
|           2             | 1                      | FAIL: duplicate events
|           1             | 2                      | FAIL: duplicate message rows
```
→ Dedupe falhou. Consulte o time de engenharia.

---

## 📊 PASSO 4: Executar QUERY 3 (Opcional — Diagnóstico Extra)

**Propósito:** Validar correlação entre webhook event e message row.

**Copie e cole no terminal `psql`:**

```sql
SELECT 
  cwe.provider_event_id,
  cwe.provider,
  cwe.event_type,
  cwe.received_at as webhook_received_at,
  m.id as message_id,
  m.provider_message_id,
  m.created_at as message_created_at,
  CASE 
    WHEN cwe.provider_event_id = m.provider_message_id THEN 'matched'
    ELSE 'mismatch'
  END as event_message_correlation
FROM channel_webhook_events cwe
  JOIN messages m ON cwe.connection_id = m.conversation_id
WHERE cwe.provider = 'waha'
  AND m.body = 'OMNIRA-E2E-P8-20260921-03'
LIMIT 5;
```

**Resultado esperado:**
```
      provider_event_id       | provider | event_type | webhook_received_at | message_id | ... | event_message_correlation
                              | waha     | message    | 2026-09-21 14:34:XX | [UUID]     | ... | matched
```

---

## 📝 PASSO 5: Compilar Relatório Final

Após executar as 3 queries, preencha o relatório abaixo com **números e UUIDs APENAS** (sem credenciais):

```
=============================================================
P8 DATABASE EVIDENCE COLLECTION — RELATÓRIO FINAL
=============================================================

Mensagem buscada: OMNIRA-E2E-P8-20260921-03
Banco de dados: [NOME DO BANCO DE PRODUÇÃO]
Data da coleta: [HOJE]
Hora da coleta: [HH:MM:SS UTC]

---

QUERY 1 RESULT (Localizar Mensagem):
✓ Encontrada? SIM / NÃO
✓ Quantidade de linhas retornadas: [0 / 1 / >1]

Se encontrada (1 linha):
  message_id = [UUID]
  provider_message_id = [STRING]
  contact_id = [UUID]
  from_number = [E.164]
  conversation_id = [UUID]
  channel_connection_id = [UUID]
  provider = [waha / outro]
  provider_kind = [unofficial / official]
  direction = [inbound / outbound]
  status = [received / other]
  created_at = [TIMESTAMP EXATO]
  exact_content_count = [1 / outro]

---

QUERY 2 RESULT (Validar Dedupe):
✓ webhook_events_received = [0 / 1 / >1]
✓ message_rows_persisted = [0 / 1 / >1]
✓ dedupe_status = [PASS: exactly-once / WARNING / FAIL]

---

QUERY 3 RESULT (Opcional — Diagnóstico):
✓ event_message_correlation = [matched / mismatch / N/A]

---

CONCLUSÃO:
✓ Mensagem persistida exatamente uma vez? SIM / NÃO
✓ Provider = WAHA confirmado? SIM / NÃO
✓ Webhook dedupe = OK? SIM / NÃO
✓ Conteúdo exato? SIM / NÃO

P8 STATUS: ✓ DONE / ❌ BLOCKED

Notas adicionais: [qualquer detalhe relevante]

=============================================================
```

---

## 🚨 ANTES DE DESCONECTAR

**Verificação de segurança:**

- ✗ NÃO copiar DATABASE_URL ou credenciais
- ✗ NÃO executar INSERT/UPDATE/DELETE
- ✗ NÃO alterar nenhum dado
- ✗ NÃO reiniciar WAHA, API ou worker
- ✗ NÃO fazer deploy

**Desconectar:**
```bash
\q  # no psql
exit # da SSH
```

---

## 📨 ONDE REPORTAR RESULTADOS

1. **Copiar o relatório acima** (PASSO 5)
2. **Postar em:** [canal/ticket/email padrão da equipe]
3. **Título:** `P8 Database Evidence Collection — REPORT`
4. **Conteúdo:** Apenas números/UUIDs (sem credenciais)

---

## ❓ TROUBLESHOOTING

### "Acesso negado" / "FATAL: role 'omnira_app' does not exist"

**Causa:** Credenciais incorretas ou usuário não existe.

**Ação:** Confirme com operador de produção:
- Host correto?
- Usuário correto? (pode ser `omnira_app` ou `omnira` ou outro)
- Senha correta?
- Banco correto?

### "0 rows" na QUERY 1

**Causa 1:** Banco remoto é diferente do esperado (produção vs. staging).

**Ação:** Confirme o host do banco remoto com operador.

**Causa 2:** Mensagem nunca foi enviada para WAHA.

**Ação:** Consulte logs de produção (webhook do WAHA, API logs).

### ">1 rows" na QUERY 1

**Causa:** Há duplicatas de mensagem (não esperado).

**Ação:** PAUSE. Consulte o time de engenharia antes de qualquer alteração.

### "WARNING: no webhook event" na QUERY 2

**Causa:** Webhook não foi recebido ou não foi persistido.

**Ação:** Verificar:
- WAHA está enviando webhooks? (`WAHA_ENABLED=true`, webhook URL configurada)
- API está recebendo webhooks? (logs da API)
- `channel_webhook_events` está sendo populada? (schema migrations aplicadas)

---

## ✅ CHECKLIST FINAL

Antes de reportar P8 como DONE:

- [ ] Conectado ao banco de produção remoto
- [ ] QUERY 1 executada: 1 linha retornada (não 0, não >1)
- [ ] QUERY 2 executado: dedupe_status = "PASS: exactly-once"
- [ ] QUERY 3 executado (opcional): event_message_correlation = "matched"
- [ ] Relatório final preenchido
- [ ] Nenhuma credencial copiada
- [ ] Nenhum dado alterado
- [ ] Desconectado do banco

---

**Versão:** 1.0  
**Data:** 2026-10-03  
**Owner:** Engineering (Claude Haiku)  
**Status:** READY FOR EXECUTION

