# IAM4.2 — Design Gate: Presence & Heartbeat

**Status:** Decisions APPROVED by human (2026-09-22). See `docs/adr/0010-agent-presence-and-heartbeat.md` (Accepted) for the binding decision. This document retains the design exploration for context; do not treat superseded proposals below as current.

**IAM4.2-A: IMPLEMENTED = YES, VERIFIED = YES (2026-09-22).** All gates PASS including full isolated E2E (Playwright 25/25, including 2 new presence specs). See `docs/delivery/HANDOFF-NEXT-AGENT.md` § IAM4.2-A for the file list and gate evidence. Next: IAM4.2-B (routing enforcement, gated rollout) — not started.

**Escopo:** Presence, heartbeat, last_seen, TTL e propagação realtime para IAM4.2.

---

## Princípios invioláveis (congelados em IAM4.1)

- **Membership** = identidade/acesso tenant
- **AgentProfile** = participação operacional opcional (1:1, status active|disabled)
- **queue_members.available** = elegibilidade para receber trabalho **naquela fila**
- **Portanto:** `queue availability ≠ human presence`
- Valkey era deferido até haver necessidade real de cache/presence — presence é essa necessidade.

---

## DECISÃO FINAL (aprovada 2026-09-22)

Ver ADR-0010 para o texto normativo. Resumo:

| Aspecto | Decisão |
|---|---|
| Presence | Boleano: `online` \| `offline` (sem away/busy) |
| Heartbeat | 30s, endpoint self-scoped `POST /me/presence/heartbeat` |
| TTL | 120s (não 5 minutos) |
| Source of truth realtime | **Valkey**, por sessão/tab |
| Postgres | Apenas `last_seen_at` coalescido; sem write por heartbeat; sem tabela `agent_presence` realtime |
| Multiple tabs | Por sessão/tab; agent online = pelo menos 1 sessão viva |
| NATS | Somente transições agregadas (`offline→online`, `online→offline`); nunca por heartbeat |
| SSE | Snapshot inicial (GET) + incremental (SSE); polling é fallback |
| Routing | Requisito final: membership + AgentProfile + queue availability + capacity + presence — **não ativado no primeiro deploy** |
| Rollout | IAM4.2-A (heartbeat/Valkey/NATS/SSE, routing inalterado) → IAM4.2-B (enforcement, pilot tenant primeiro) |
| Failure (pós-enforcement) | Valkey indisponível → routing automático fail-closed; claim/assign manual inalterados |
| Auditoria | Heartbeat individual NÃO é auditado; apenas transições agregadas observáveis |
| Deferred | away/busy, skills, analytics de presença, performance |

**A proposta anterior "Postgres-first" (write por heartbeat, Postgres como fonte de verdade realtime) foi rejeitada por decisão humana.** Mantida abaixo apenas como registro histórico do design gate.

---

## Design Exploration (histórico — pré-decisão)

As seções abaixo documentam as opções consideradas antes da decisão final. Servem de contexto, não de especificação.

### 1. O QUE é "Presence"? — RESOLVIDO: boleano online/offline

### 2. HEARTBEAT — RESOLVIDO: automático, 30s, self-scoped

### 3. TTL — RESOLVIDO: 120s

### 4. LAST_SEEN / Storage — RESOLVIDO: Valkey (realtime) + Postgres (coalescido, durável)

### 5. FONTE DE VERDADE — RESOLVIDO: Valkey para presença realtime; Postgres nunca é consultado para decisão online/offline

### 6. ARMAZENAMENTO — RESOLVIDO: ephemeral (Valkey, TTL nativo) + checkpoint durável (Postgres, last_seen_at)

### 7. PROPAGAÇÃO REALTIME — RESOLVIDO: NATS (transições agregadas) + SSE (snapshot + incremental); polling é fallback

### 8. RELAÇÃO COM AGENT PROFILE — RESOLVIDO: independente; presence não é atributo de `agent_profiles`

### 9. RELAÇÃO COM QUEUE_MEMBERS.AVAILABLE — RESOLVIDO: separado; routing eventualmente exige ambos, mas não no primeiro deploy

### 10. EFEITO NO ROUTING — RESOLVIDO: hard requirement fica para IAM4.2-B, com rollout controlado no pilot tenant; IAM4.2-A não altera routing

---

## Proposta Postgres-first (SUPERSEDIDA — não implementar)

A proposta original abaixo foi avaliada e **rejeitada**. Preservada para histórico de decisão, não como opção viva.

| Aspecto | Proposta original (rejeitada) |
|---|---|
| Heartbeat | Write direto em Postgres a cada 30s |
| TTL | 5 minutos |
| Fonte de verdade | Postgres `agent_presence` table |
| NATS | Publicar por heartbeat |

Motivo da rejeição: presence é estado efêmero; write-per-heartbeat em Postgres é carga desnecessária e o modelo errado de fonte de verdade para um sinal transiente. Valkey resolve isso (consistente com `docs/adr/0006`).

---

## Escopo de Implementação

### IAM4.2-A (primeira slice)

- `POST /me/presence/heartbeat` (self-scoped, resolve tenant/membership/AgentProfile da sessão)
- Valkey: chave por sessão/tab, TTL 120s
- Persistência coalescida de `last_seen_at` em Postgres (sem tabela `agent_presence` realtime)
- NATS: evento apenas em transição agregada por agent (não por sessão, não por heartbeat)
- SSE: endpoint supervisor com snapshot inicial + incremental
- Routing: **inalterado** — não consulta presence
- Sem auditoria de heartbeat individual

### IAM4.2-B (rollout controlado, gate separado)

- Ativar presence como requisito de elegibilidade no routing
- Rollout primeiro no pilot tenant
- Failure semantics: Valkey indisponível → routing automático fail-closed; manual claim/assign inalterados

### Fora de escopo (deferred)

- Dashboard de presença (UI)
- away/busy / status manual
- Analytics/histórico de presença
- Skills (IAM4.3)
- Global capacity (defer se sem ADR explícito)
- Migração do legado `/users/agents`

---

## Próximos Passos

1. ✅ ADR-0010 promovido para Accepted
2. Migration: schema mínimo para `last_seen_at` coalescido (sem tabela de presence realtime em Postgres)
3. Implementation IAM4.2-A: heartbeat endpoint, Valkey integration, NATS transition events, SSE presence stream
4. Tests: heartbeat → Valkey TTL → transition detection; multi-sessão (fechar 1 tab não derruba presence); RLS/authz do endpoint self-scoped
5. IAM4.2-B fica para gate separado, após IAM4.2-A validado

---

## Referências

- `docs/adr/0010-agent-presence-and-heartbeat.md` — decisão normativa (Accepted)
- `docs/delivery/HANDOFF-NEXT-AGENT.md` — IAM4 roadmap e rollout status
- `docs/delivery/IAM4-AGENT-MANAGEMENT.md` — IAM4.1 decisions
- `docs/adr/0006-nats-jetstream-and-postgres-source-of-truth.md` — Valkey policy
- `internal/worker/realtime/bridge.go` — SSE infrastructure (padrão reutilizado para transições)
- `migrations/000038_agent_profiles.up.sql` — agent_profiles schema
