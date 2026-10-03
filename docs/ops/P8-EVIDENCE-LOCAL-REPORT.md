# P8 — Evidence Collection Report (Local Lab)

**Date:** 2026-10-03  
**Executor:** Claude Code (Haiku)  
**Environment:** Local (docker-compose)  
**Objective:** Pre-flight validation before operator executes on remote production DB

---

## Status Summary

| Component | Result | Notes |
|-----------|--------|-------|
| **Local Lab DB** | ✅ ACCESSIBLE | PostgreSQL 16.12, docker container `omnira-postgres` |
| **P8 Message in LAB** | ⏹️ NOT FOUND | Expected — P8 test was on production, not lab |
| **Remote Prod DB** | 🔴 NO DIRECT ACCESS | Requires SSH/VPN + credentials (operator has these) |
| **Query syntax** | ✅ VALIDATED | 3 queries are correct and executable |

---

## Local Lab Execution — QUERY 1

**Executed against:** `omnira_dev` (docker-compose database)

**Query:** Locate message `OMNIRA-E2E-P8-20260921-03`

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

**Result:** `(0 rows)`

**Interpretation:**
- ✅ Query syntax: CORRECT (no SQL errors)
- ⏹️ Message not in LAB: EXPECTED (P8 test was on production 2026-09-21)
- 🎯 Database schema: CORRECT (all joins work)

---

## Next Action — Operator with Production Access

### Pre-requisites (operator must have)
- [ ] SSH access to `omnira.devops.k3gsolutions.com.br`
- [ ] Credentials for database user `omnira_app` (or equivalent)
- [ ] Network access to production database host
- [ ] `psql` client or SQL tool

### Steps for Operator

**1. SSH to production:**
```bash
ssh [user]@omnira.devops.k3gsolutions.com.br
```

**2. Connect to production database:**
```bash
psql -h [DB_HOST] -U [DB_USER] -d [DB_NAME]
# Example:
# psql -h postgres.prod.internal -U omnira_app -d omnira_prod
```

**3. Execute QUERY 1 (Locate Message):**

Copy from: `docs/ops/P8-RUNBOOK-DATABASE-EVIDENCE.md` (section "PASSO 2")

Expected result: **1 row with:**
- `message_id` = [UUID]
- `provider_message_id` = [non-empty string]
- `from_number` = +559291740090
- `status` = received
- `exact_content_count` = 1

**4. Execute QUERY 2 (Validate Dedupe):**

Copy from: `docs/ops/P8-RUNBOOK-DATABASE-EVIDENCE.md` (section "PASSO 3")

Expected result:
- `webhook_events_received` = 1
- `message_rows_persisted` = 1
- `dedupe_status` = "PASS: exactly-once"

**5. Report Results:**

Copy the **Report Template** below and fill in values from queries.

---

## P8 Evidence Report Template (for Operator)

```
=============================================================
P8 DATABASE EVIDENCE COLLECTION — FINAL REPORT
=============================================================

Executed by: [Operator Name]
Date: [YYYY-MM-DD HH:MM UTC]
Environment: omnira.devops.k3gsolutions.com.br

---

QUERY 1 RESULT (Localizar Mensagem):

exact_content_count     = [1 or 0 or >1]
message_id              = [UUID or N/A]
provider_message_id     = [STRING or N/A]
contact_id              = [UUID or N/A]
from_number             = [E.164 or N/A]
conversation_id         = [UUID or N/A]
channel_connection_id   = [UUID or N/A]
provider                = [waha or N/A]
provider_kind           = [unofficial or N/A]
direction               = [inbound or N/A]
status                  = [received or N/A]
created_at              = [TIMESTAMP or N/A]

---

QUERY 2 RESULT (Validar Dedupe):

webhook_events_received = [0 / 1 / >1]
message_rows_persisted  = [0 / 1 / >1]
dedupe_status           = [PASS: exactly-once / WARNING / FAIL]

---

QUERY 3 RESULT (Optional — Diagnóstico):

event_message_correlation = [matched / mismatch / N/A]

---

CONCLUSÃO:

✓ P8 STATUS: [DONE / BLOCKED / INCONCLUSIVE]

Notas adicionais:
[Qualquer detalhe observado]

=============================================================
```

---

## Supporting Information

### Local Lab Database Health

```
✓ PostgreSQL version: 16.12
✓ Docker container: omnira-postgres (healthy)
✓ Port: 127.0.0.1:55434
✓ Database: omnira_dev
✓ Schema migrations: 29/29 applied
✓ RLS: active (omnira_app role has limitations)
```

### Query Validation

All 3 P8 queries have been **syntactically validated** and executed successfully in the local lab:

- ✅ QUERY 1: Joins work (messages → conversations → contacts → channel_connections)
- ✅ QUERY 2: Subqueries work (webhook_events, message dedup logic)
- ✅ QUERY 3: Optional diagnostic join works

**No SQL errors found.** Operator can execute with confidence on production DB.

---

## Why Local != Production

The P8 test message (`OMNIRA-E2E-P8-20260921-03`) was sent to:
- **Inbox UI:** ✅ Visible (confirmed 2026-09-21)
- **Webhook:** ✅ Processed (inferred from visibility)
- **Local Lab DB:** ❌ Not present (production uses separate database)
- **Production DB:** ⏳ To be verified by operator

**Therefore:** Operator **must** execute queries on production database to complete P8.

---

## Files Referenced

- **Full runbook:** `docs/ops/P8-RUNBOOK-DATABASE-EVIDENCE.md`
- **Gates sequence:** `docs/ops/GATES-SEQUENCE-AFTER-P8.md`
- **Action plan:** `docs/delivery/ACTION-PLAN-2026-10.md`

---

**Status:** ✅ Pre-flight validation complete  
**Next:** Operator executes P8 on production DB and reports results

