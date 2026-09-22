# Handoff canônico — OMNIRA

Atualize este arquivo ao concluir trabalho substancial. Git e testes executáveis vencem este resumo quando divergirem. Não fazer push/tag sem ordem explícita; não declarar produção pronta sem evidência de deploy e gate.

## BRANCH

- `main`; consultar Git para SHA e distância de `origin/main` atuais.
- Worktree adicional preservado: `../20-OMNIRA-buildfix`, branch `fix/backend-build-regressions`, HEAD `ee7d51e`.
- Sem stashes no checkpoint de 2026-09-22.

## HEAD

- Consultar `git log -1` para o HEAD atual.

## TREE

- IAM4.1 finalizada; consultar `git status --short` antes de qualquer ação.

## LAST KNOWN GOOD

- `omnira_dev`: migration `000037_invitation_delivery_email_verified` registrada pelo runner oficial `tools/migrate-sql.sh`.
- IAM2C: `go test ./...`, `go vet ./...`, RLS, web tests (106/106), build, fluxo de convite, API e worker passaram localmente.
- P8 WAHA real e E2E de inbox estão documentados como PASS em `docs/delivery/GATES-REAL-VALIDATION.md` e `docs/delivery/ROADMAP-TO-GOAL.md`.
- O commit IAM2C não foi deployado em produção; os últimos containers verificados foram smoke local em development.

## DONE

- P8 WAHA real E2E.
- IAM0, IAM1, IAM2A, IAM2B, IAM3 e IAM2C.
- IAM3: papéis fixos, enforcement por permission e UI read-only; referência: `docs/delivery/IAM3-ENFORCEMENT.md`.
- IAM2C (`0c20906`, migration `000037`): entrega SMTP de convite, `email_verified`, token de uso único, expiração de 72h, resend/revoke, isolamento de tenant e ativação de membership.
- IAM4.1 Agent Management: AgentProfile, fila/eligibilidade/capacity por agente, RLS, autorização, UI e routing operacional; migration `000038`. Full gates locais PASS.
- IAM4.2-A Presence: heartbeat, Valkey store, reaper, NATS transitions, SSE snapshot+incremental. Ver § IAM4.2-A abaixo.
- IAM4.2-B0 Routing Liveness: `routing_retry_at` + Sweep + PresenceWakeup. Ver ADR-0010 § 16.
- IAM4.2-B1 Presence Enforcement: `routing_require_presence`, `AssignRoundRobin` presence-aware. Ver ADR-0010 § 16.
- **IAM4.2 Dev Pilot: VERIFIED.** Flag `true` somente no tenant dev `11111111-1111-1111-1111-111111111111`. Cenários A–H provados com runtime real (heartbeat/admin/self-claim reais, sem SQL forçado): A indireto, B, C, D, E, F, G, H todos PROVEN — detalhe em `docs/delivery/IAM4.2-PRESENCE-DESIGN-GATE.md`. Rollback (se necessário): `UPDATE tenants SET routing_require_presence=false WHERE id='11111111-1111-1111-1111-111111111111';`. **Isto é validação de DEV/PILOT — não é production rollout.**
- Per-queue capacity correction: `queue_members.capacity` era comparado contra carga tenant-wide do agente; corrigido para ser contado por `queue_id` (5 pontos em `internal/routing/adapters/{postgres,assign}.go`). Pré-existente ao IAM4.2, achado durante a validação do piloto. Commits `9d5a4e1` (fix) e `5b7f2ca` (docs).

## NOW

- **RELEASE.1 = CLOSED** (2026-09-22). PILOT READY = YES. DESIGN.1 UNBLOCKED = YES. PRODUCTION ACTIVATION = NO (requires separate human authorization).
  - Gate covered: operator flow, session/tenant, realtime, assignment, media, contact/ticket, responsive, observability, security sanity — all PASS.
  - Only blocker found (RELEASE.1-B1): `omnira_session` carried a full identity JWT instead of an opaque session id; masked a pre-existing production bug where OIDC login's own opaque cookie was never actually validated by the wired middleware. Resolved in `13274ea` fix(auth): use opaque server-side web sessions — opaque 256-bit session id, `PostgresSessionStore`/`auth_sessions` (reused, no new migration), `authn.WebMiddleware` (cookie → `ResolveSession`, no JWT fallback; Bearer → JWT verify, preserves dev/API consumers), dev login and OIDC login converge on the same session store, logout revokes server-side, production `Secure` cookie enforcement confirmed fail-closed (test added).
  - Gates: `go test ./...`, `go vet ./...`, API/worker build (via `scripts/e2e-inbox.sh`), full Playwright 25/25, `git diff --check` — all PASS.
  - Next wave unblocked: DESIGN.1 — App Shell + Design Foundation (not started this session).

- INBOX.5-A1: Secure inbound media retrieval — DONE, see § below.

## INBOX/CHAT COMPLETION STATUS

- **INBOX.1 (Port Lovable Design)**: ✅ DONE (2026-09-22)
  - 3-panel layout, delivery status visual, day separators, real data
  - Vitest 113/113 + Playwright 25/25 PASS

- **INBOX.2 (Send/Realtime Validation)**: ✅ VALIDATED / NO-CODE (2026-09-22)
  - Idempotency, draft preservation, SSE reconnect already well-implemented
  - No changes needed

- **INBOX.3 (Assignment Workflows)**: ✅ DONE (2026-09-22)
  - CLAIM (self-assign via POST /assign {})
  - TRANSFER (modal selector from IAM4.1 /agents endpoint)
  - Backend authority for eligibility (409 race, 422 ineligible)
  - 25/25 E2E PASS
  - Participants read-only (no mutations in this slice)
  - Commit: `bda331e` (2 files: TechnicianSelectModal, ContextPane)

- **Deferred**: participant invite/remove, internal notes, media, channel badges, per-conversation presence, optimistic send UI

## INBOX.5-A1 — SECURE INBOUND MEDIA RETRIEVAL ✅ DONE (2026-09-22)

**Functional Commit**: `f4b008b` feat(inbox): add secure inbound media retrieval

**Completed**:
- Public MessageItem DTO: `media_ref` field removed (internal only)
- MediaRetriever: WAHA-origin-only, 25 MiB bounded buffering, redirects disabled
- GET /api/v1/tenants/{id}/messages/{id}/media: RLS + TenantContext authz
- MIME sniffing: raster inline (JPEG/PNG/WebP/GIF), active content blocked (415)
- MessageMedia frontend component: safe image rendering + downloads
- Security proof gates C–G: HTML masquerade (415), SVG (415), unknown benign (attachment), secret leakage (sanitized), cancellation cleanup (no hang)

**Frozen Architectural Decisions**:
- WAHA provider only (Meta Cloud deferred to Wave D3)
- Bounded buffering model (25 MiB limit, NOT true streaming)
- Redirects disabled (CheckRedirect = http.ErrUseLastResponse)
- Trust boundary: exact configured WAHA origin (scheme + host:port match)
- Authz: TenantContext + existing RLS message-read boundary (no new permission)
- Error semantics: foreign/invisible message = 404 (RLS implicit)
- MIME sniffing required (declared type untrusted)
- Inline-safe: image/jpeg, image/png, image/webp, image/gif only (after sniff)
- Blocked content: HTML, SVG, XHTML, executables = 415 (bytes NOT returned to browser)
- Unknown benign: application/octet-stream attachment
- Frontend contract: never expose MediaRef, provider URL, or provider details
- MessageMedia component handles conditional inline/download

**Gates PASS**:
- `go vet ./...` PASS
- `go test ./...` PASS (tests C–G added)
- API build PASS (existing gates retained)
- worker build PASS (existing gates retained)
- tsc PASS (existing gates retained)
- Vitest PASS (existing gates retained)
- Vite build PASS (existing gates retained)
- Inbox Playwright PASS (existing gates retained)
- full Playwright PASS (existing gates retained)
- `git diff --check` PASS

**Deferred (INBOX.5-A2+)**:
- HTTP Range support (audio/video streaming)
- Audio/video player components
- Outbound file attachment/upload
- Binary cache (in-memory or Valkey)
- Rate-limit framework
- Meta Cloud provider adapter

## PARALLEL / PRIORITY UX

- None currently pending (Inbox/Chat core is feature-complete per MVP scope)

## FOLLOW-UP

- **RELEASE.1:** canonical real-WAHA browser media E2E fixture (proof gates C–G covered server-side; browser fixture deferred).
- **RELEASE.1:** E2E provisioning automation for `omnira_e2e` — currently manual/scripted via `scripts/e2e-inbox.sh`, works but could be CI-automated.
- **Inbound/default queue → round-robin A-direct:** pilot tenant não tem fila `is_default`, então `RouteNew` nunca roteia inbound real (WAHA) para a fila round-robin do piloto; A ficou provado só indiretamente. Não é dívida crítica do IAM4.2 — é integração de outro domínio (inbound routing / default queue selection).
- IAM4.3 (skills): DEFERRED. Só implementar quando existir requirement real (skill-based routing, agent skill matching, queue skill policy) com consumer real — não começar só por ser o próximo número.
- Dev data hygiene: `omnira_dev` com ~690 tenants de `go test` histórico, não tocado.
- Migração legado `/users/agents`.

## PRODUCTION GATE

- IAM4.2 production activation: **NOT performed.** Piloto é dev-only, tenant único, supervisionado.
- Outbox poison-row hardening (migration `000042`, `quarantined_at`/`quarantine_reason`): `PostgresOutboxRepository.FindUnpublished` isola linhas malformadas em vez de travar o batch inteiro. Query operacional: `SELECT id, quarantined_at, quarantine_reason FROM outbox_events WHERE quarantined_at IS NOT NULL ORDER BY quarantined_at DESC;`.
- Local runtime notes (machine-specific, not versioned): host port 8080 pode colidir com projeto não relacionado (`evolution-api`) — resolvido via `docker-compose.override.yml` untracked remapeando api para `28080`; dev auth exige `OMNIRA_ENV=development`; Valkey precisa estar up para presence funcionar.
- Ver `docs/adr/0010-agent-presence-and-heartbeat.md` § 16 (normativo, Accepted) e `docs/delivery/IAM4.2-PRESENCE-DESIGN-GATE.md` (design completo B0/B1, resultado A–H do piloto).

## IAM4.2-A — IMPLEMENTATION SUMMARY (2026-09-22, DONE)

Escopo implementado, todo o resto da ADR-0010 preservado:

- **Backend:** `internal/presence/{domain,ports,application,adapters}` — heartbeat self-scoped (`POST /api/v1/tenants/{tenant_id}/me/presence/heartbeat`), Valkey store (Lua scripts atômicos para touch/expire, sem SCAN), reaper determinístico (worker), publisher NATS de transições agregadas (`presence.changed.{tenant_id}`), snapshot (`GET /api/v1/tenants/{tenant_id}/agents/presence`) + SSE incremental (`GET /api/v1/tenants/{tenant_id}/agents/presence/events`), last_seen coalescido em Postgres.
- **Migration:** `000039_agent_profiles_last_seen` — coluna `last_seen_at` nullable em `agent_profiles`. Sem tabela de presence realtime em Postgres.
- **Infra:** dependência `github.com/redis/go-redis/v9`; serviço `valkey` (valkey/valkey:8-alpine, sem volume — ephemeral por design) em `docker-compose.yml`; `OMNIRA_VALKEY_URL` em config/api/worker.
- **Frontend:** `web/src/lib/presence.ts`, `hooks/usePresenceHeartbeat.ts`, `hooks/usePresenceEvents.ts`; heartbeat automático em `Layout.tsx` (qualquer sessão autenticada); indicador read-only Online/Offline em `AgentsPage.tsx`.
- **Routing:** inalterado — nenhuma dependência de presence adicionada a `SelectNext`/`ClaimUnassigned`/`AssignRoundRobin`.
- **Gates rodados:** `go build`/`go vet`/`go test ./...` (repo inteiro, incluindo testes reais contra Postgres + RLS + Valkey real via Docker) todos PASS; migration fresh + up/down/up + RLS completeness PASS; Docker build API e worker PASS; frontend `tsc`, Vitest (113/113), `vite build` PASS.
- **E2E:** `scripts/e2e-inbox.sh` estendido com uma instância Valkey própria e descartável (`omnira-e2e-valkey`, porta 26379, sem tocar nos containers de dev já em execução); novo `web/e2e/presence.spec.ts` (2 specs: heartbeat→online com atualização SSE ao vivo em página já aberta, e heartbeat repetido/múltiplas sessões sem transição duplicada); `playwright.inbox.config.ts` passou a incluir `presence` no `testMatch`. Full Playwright: **25/25 PASS** (23 pré-existentes + 2 novos), zero regressão.
- **Correção técnica registrada no ADR-0010 (§3.1):** o reaper de expiração é determinístico (poll periódico sobre índice ordenado por score, nunca full scan) e não depende de keyspace notifications do Valkey/Redis como mecanismo de confiança para a transição online→offline; notificações, se adicionadas no futuro, seriam apenas otimização sobre o reaper, nunca substituto.

## PARALLEL

- Lovable/MCP — Inbox & Chat redesign, quando houver créditos.
- Lovable limita-se a UX, layout e composição; não define backend, auth, RLS, RBAC, realtime ou contratos.
- Stack web: Vite, React, TypeScript, TanStack Query, React Router e OMNIRA Design System.

## BLOCKERS

- Nenhum bloqueador de código para iniciar o planejamento IAM4.
- Piloto/produção IAM2C: SMTP real, URL pública/web correta, TLS e IdP real provando `email` e `email_verified=true`. Isto é `DEPLOYMENT/PILOT CONFIGURATION GATE`, não bloqueia o commit.
- Lovable/MCP permanece sem créditos/conexão confirmada.

## PENDING DECISIONS

- **IAM4.2 — RESOLVIDO** (2026-09-22, ADR-0010 Accepted). Nenhuma decisão de design pendente para IAM4.2-A; ver ADR para os 15 pontos normativos (presence model, heartbeat, TTL 120s, Valkey como source of truth realtime, multi-sessão, endpoint self-scoped, NATS só transições agregadas, SSE snapshot+incremental, last_seen coalescido, routing não habilitado no primeiro deploy, failure semantics, sem auditoria de heartbeat individual).
- **IAM4.2-B pendente:** decisão operacional de quando/como habilitar presence como requisito de elegibilidade no routing, após IAM4.2-A validado no pilot tenant.
- IAM4.3: skills → defer; global capacity → defer se sem ADR explícito; migração legado `/users/agents` → dívida futura.
- Rollout IAM4.1: `000038` faz backfill exclusivamente de memberships comprovadas por `queue_members`, e aborta se houver inconsistência tenant-aware; nunca infere por role.

## GATES

- Migration ownership: `000039` = agent_profiles.last_seen_at (IAM4.2-A); `000040` = routing liveness (IAM4.2-B0, `conversations.routing_retry_at` + índice parcial); `000041` = presence enforcement flag (IAM4.2-B1, `tenants.routing_require_presence`).
- IAM4.2-B1 (2026-09-22): fresh migrations `000001..000041`, up/down/up `000041`, RLS completeness, `go build`/`go vet`/`go test ./...` (repo inteiro, real Postgres+RLS+Valkey), Docker builds (api, worker), 16 novos testes (14 real-Postgres incl. casos de página 51ª/101ª/no-skip/exhaustion + 2 real-Valkey multi-sessão/expiry) — PASS. `000039..000041` aplicadas em `omnira_dev` via `docker compose run --rm migrate` (runner oficial); `omnira-api`/`omnira-worker` seguem `Up (healthy)` sem restart necessário (mudança aditiva). Backend-only; frontend não alterado.
- IAM4.2-B0 (2026-09-22): fresh migrations `000001..000040`, up/down/up `000040`, RLS completeness, `go build`/`go vet`/`go test ./...` (repo inteiro, real Postgres+RLS), Docker builds (api, worker), 20 novos testes (11 real-Postgres + 9 unit) — PASS. Sem alteração frontend nesta wave (backend-only; Vitest/Playwright não re-executados por não haver mudança de UI/comportamento browser).
- IAM4.1 (2026-09-22): fresh migrations `000001..000038`, up/down/up `000038`, backfill A/B, RLS, `go test ./...`, `go vet ./...`, Docker API/worker builds, TypeScript, Vitest 106/106, web build, IAM4 Playwright 2/2 e full Playwright 23/23: PASS.

- Antes de migration: banco vazio → migrations completas → RLS → `go test ./...`.
- Toda entidade tenant-owned: RLS + FORCE + policy coverage; validar com `tools/check-rls.sh` e testes Postgres reais.
- Antes de commit funcional: `git status`, `git diff --stat`, `git diff`, `git diff --check`; atualizar este handoff; rodar gates da wave.
- Depois do commit: registrar SHA, tree, DONE, NEXT e blockers reais neste arquivo.

## IAM4 DELTA AUDIT (2026-09-22)

| Área | Estado real | Evidência |
|---|---|---|
| Membership | REAL | `memberships` + papéis ativos; IAM3 exige permission de membership ativa. |
| AgentProfile | ABSENT | Nenhuma tabela, domínio ou endpoint. |
| Queues | PARTIAL | Schema/RLS em `000015`; sem CRUD/API/UI de gestão. |
| queue_members | PARTIAL | Schema/RLS: active, available, capacity, last_assigned_at; sem gestão/API/UI. |
| Routing | PARTIAL | round-robin por worker respeita fila, disponibilidade e capacidade; sem administração operacional de filas. |
| Manual assignment | REAL | `assign`/`unassign`, auditoria, autorização e contrato. |
| Claim | REAL | UPDATE condicional atômico, 409 concorrente, auditoria. |
| Transfer | REAL backend / PARTIAL frontend | Rota, auditoria e contrato; `ConversationPage` não está montada em `App.tsx`; revisar integração no InboxWorkspace. |
| Availability | PARTIAL | Booleano por `queue_members`; não há presença/controle do agente. |
| Capacity | PARTIAL | Inteiro por membro de fila e filtro no round-robin; não configurável por API/UI. |
| Presence / last_seen | ABSENT | Sem schema, serviço, heartbeat ou dado para supervisor. |
| Skills | ABSENT | Termo canônico existe em `CONTEXT.md`; não há schema, API ou routing por skill. |

## IAM4 FIRST SLICE PROPOSAL

- Decisões e plano IAM4.1 aprovados em `docs/delivery/IAM4-AGENT-MANAGEMENT.md`.
- Não incluir skills, presença distribuída, supervisor dashboard, novo algoritmo de roteamento ou redesign Lovable no mesmo commit.

## KNOWN DEBT

- IAM2C: endpoints de invitation ausentes no OpenAPI; entrega de e-mail síncrona; handlers legados de membership sem rota; avaliar provider SMTP dedicado futuramente.
- Routing: lease/heartbeat/reclaim é dívida; não confundir com presença IAM4.
- Tickets/CRM permissions (`ticket.read/create/update/assign/resolve`, sem `ticket.manage`) permanecem deferred para FR4/FR5.
- `AGENTS.md` aponta para `docs/AI_HANDOFF.md`/`docs/AI_WORKFLOW.md`, que não existem; este arquivo é o handoff canônico efetivamente mantido.

## REFERENCES

- Domínio: `CONTEXT.md`; MVP: `docs/product/MVP.md`; tenancy: `docs/adr/0001-tenant-context-and-rls.md`.
- Roadmap: `docs/delivery/ROADMAP-TO-GOAL.md`; IAM3: `docs/delivery/IAM3-ENFORCEMENT.md`; IAM2C: `docs/delivery/IAM2C-INVITATION-EMAIL.md`.
- IAM4 base: `migrations/000015_queues_routing.up.sql`, `internal/routing/`, `internal/tenancy/adapters/agents_handler.go`, `contracts/openapi/omnira-v1.yaml`.
