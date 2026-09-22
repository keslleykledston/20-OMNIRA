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
- IAM4.1: AgentProfile, fila/eligibilidade/capacity por agente, RLS, autorização, UI e routing operacional; migration `000038`. Full gates locais PASS.

## NOW

- IAM4.1 DONE; nenhum trabalho de implementação ativo nesta slice.

## NEXT

- IAM4.2 — design gate. Presence, heartbeat/last_seen e global capacity exigem decisão própria; skills fica para IAM4.3.

## PARALLEL

- Lovable/MCP — Inbox & Chat redesign, quando houver créditos.
- Lovable limita-se a UX, layout e composição; não define backend, auth, RLS, RBAC, realtime ou contratos.
- Stack web: Vite, React, TypeScript, TanStack Query, React Router e OMNIRA Design System.

## BLOCKERS

- Nenhum bloqueador de código para iniciar o planejamento IAM4.
- Piloto/produção IAM2C: SMTP real, URL pública/web correta, TLS e IdP real provando `email` e `email_verified=true`. Isto é `DEPLOYMENT/PILOT CONFIGURATION GATE`, não bloqueia o commit.
- Lovable/MCP permanece sem créditos/conexão confirmada.

## PENDING DECISIONS

- IAM4.2+: presence/heartbeat/last_seen, global capacity e dashboard realtime continuam fora da slice; skills → IAM4.3; migração do legado `/users/agents` futura.
- Rollout IAM4.1: `000038` faz backfill exclusivamente de memberships comprovadas por `queue_members`, e aborta se houver inconsistência tenant-aware; nunca infere por role.

## GATES

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
