# Source Map — DeskcommCRM → OMNIRA

**Repositório doador:** https://github.com/melgarafael/DeskcommCRM
**SHA capturado:** `04ef7981cba1c5f87422cf79f8ad44454e731d4c` (branch `main`)
**Data da captura:** 2026-09-19T02:16:26Z
**Clone de referência (não faz parte do runtime OMNIRA):** `.reference-external/DeskcommCRM/` (fora de `internal/`, `apps/`, `web/` — nunca importado por código de produção)

> Esta é a Fase 1 (Audit only) do `OMNIRA-DESKCOMM-REUSE-KIT-v1.1`. Nada foi copiado para dentro do OMNIRA nesta fase. Quando a primeira cópia/adaptação substancial de qualquer linha desta tabela acontecer (Wave D1 em diante), abrir `THIRD_PARTY_NOTICES.md` na raiz do OMNIRA citando `source_repo`, `source_path` e `source_commit` (este SHA), conforme `docs/reference-kits/deskcomm-reuse/OMNIRA-DESKCOMM-REUSE-KIT-v1.1/docs/LEGAL-ATTRIBUTION.md`.

| OMNIRA target (proposto) | Deskcomm source_path | source_commit | classification |
|---|---|---|---|
| `docs/decisions/` (ADR) + `internal/tenancy` (já implementado) | `docs/specs/01-spec-platform-base.md` §2.7–3.5 (RLS helpers + templates A–D) | 04ef7981 | INSPIRE |
| `docs/decisions/` (ADR) | `docs/specs/01-spec-platform-base.md` §5 (matriz RBAC viewer<agent<manager<admin) | 04ef7981 | INSPIRE |
| `internal/platform/idempotency` (a criar) | `docs/specs/01-spec-platform-base.md` §7.3 (Idempotency-Key: reserva vs recibo) | 04ef7981 | PORT |
| `internal/platform/pagination` (já existe, revisar) | `docs/specs/01-spec-platform-base.md` §7.2 (cursor HMAC-protected) | 04ef7981 | INSPIRE |
| `internal/platform/ratelimit` (já existe, revisar) | `docs/specs/01-spec-platform-base.md` §7.4 (sliding window + fallback in-memory) | 04ef7981 | INSPIRE |
| `docs/architecture/API-GOVERNANCE.md` (a alinhar) | `docs/specs/01-spec-platform-base.md` §7.5 (tabela de error codes canônicos) | 04ef7981 | INSPIRE |
| `tools/check-rls` (a criar) + `tests/isolation/` | `tests/invariants/rls-isolation.test.ts` (padrão countAs/JWT simulado por tabela) | 04ef7981 | PORT (spec, não o TS) |
| `tools/check-rls` (a criar) | `tests/invariants/rls-completude-varredura.test.ts` (varredura de catálogo + 2 listas de exceção nomeadas) | 04ef7981 | PORT (spec, não o TS) |
| `internal/channels/` (a criar) | `lib/channels/types.ts` (`ChannelAdapter`, `ChannelCapabilities`, `OutboundEnvelope`) | 04ef7981 | PORT |
| `internal/channels/meta/` (a criar) | `lib/channels/adapters/meta-cloud.ts` | 04ef7981 | PORT |
| `internal/channels/meta/` (a criar) | `lib/channels/meta/` (credentials.ts, webhook.ts, ingest.ts, session.ts, template-*.ts) | 04ef7981 | PORT (parcial — ver REUSE-AUDIT) |
| N/A — não portar | `lib/channels/adapters/waha*.ts` (via `lib/waha/`) | 04ef7981 | REJECT como default; ADAPT futuro opcional (Wave pós-D3) |
| `apps/web/features/inbox/` (Next.js, a criar) | `app/app/inbox/` (rotas) | 04ef7981 | ADAPT |
| `apps/web/features/inbox/components/` (a criar) | `components/inbox/*.tsx` (ConversationList, ChatThread, Composer, CRMSidePanel, media/*) | 04ef7981 | ADAPT |
| `internal/bpo` (rotas de ticket, já parcialmente existe) | `docs/specs/04-spec-pipeline-attendance.md` §9 (claim atômico "Eu cuido" — UPDATE condicional + 409) | 04ef7981 | PORT |
| `internal/bpo` (routing/queue, a expandir) | `docs/specs/13-spec-governanca-atendimento.md` §5 (manual + round_robin, visibility_mode, fila por `last_inbound_at`) | 04ef7981 | INSPIRE |
| `internal/bpo` (métricas, a expandir) | `docs/specs/13-spec-governanca-atendimento.md` §6 (fórmulas de métricas por atendente, janelas semiabertas) | 04ef7981 | PORT (spec) |
| `docs/delivery/DEFINITION-OF-DONE.md` (a comparar) | `docs/specs/13-spec-governanca-atendimento.md` Apêndice A (invariantes de governança executáveis, `it.fails` como catraca de gap conhecido) | 04ef7981 | INSPIRE |
| `internal/automation/` (a criar, M07) | `lib/automation/engine.ts`, `lib/automation/types.ts`, `lib/automation/actions/`, `lib/automation/conditions.ts` | 04ef7981 | PORT |
| `internal/automation/` (a criar, M07) | `lib/followup/engine.ts`, `lib/followup/graph-schema.ts`, `lib/followup/node-handlers.ts` | 04ef7981 | PORT (parcial) |
| N/A | `.agents/skills/deskcomm-*` | 04ef7981 | INSPIRE |
| `docs/delivery/` / `CLAUDE.md` (a comparar) | `AGENTS.md`, `CLAUDE.md` (doutrina DIRC, anti-patterns, packaging, migrations idempotentes) | 04ef7981 | INSPIRE |
| N/A — não portar | Supabase Auth/Realtime/Storage, Next.js Route Handlers como backend, `event_log`+cron como fila principal | 04ef7981 | REJECT |

## Notas de leitura

- "PORT (spec, não o TS)" significa: a lógica/invariante vale como especificação a reimplementar em Go/SQL; o arquivo `.test.ts` em si não é portado literalmente (framework Vitest, sintaxe TS, chamadas `docker exec psql` específicas do ambiente Deskcomm).
- Todo item marcado ADAPT no frontend pressupõe, antes de qualquer código: remover Supabase client, trocar data layer para a API Go do OMNIRA, e aplicar o Omnira iOS Design System (ver `docs/reference-kits/deskcomm-reuse/OMNIRA-DESKCOMM-REUSE-KIT-v1.1/docs/FRONTEND-ADAPTATION.md`, ainda não lido nesta fase).
- `lib/channels/meta/` tem 19 arquivos; a auditoria (`REUSE-AUDIT.md`) detalha quais sub-arquivos são PORT direto de comportamento (webhook signature, idempotência, media SSRF allowlist) vs. os que dependem de conceitos exclusivos do Deskcomm (`contract-hash.ts`, `template-sync.ts` — amarrados ao modelo de "definições aprovadas" com espelho em Postgres próprio) e por isso são INSPIRE, não PORT direto.
