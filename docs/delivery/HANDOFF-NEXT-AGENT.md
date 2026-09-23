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

- **PRODUCT.2 = DONE** (2026-09-23). Canonical Ticket Listing — PRODUCT.2-A (`e122a34` feat(iam): add tenant ticket read permission) + PRODUCT.2-B (`77ecdb3` feat(tickets): add canonical tenant ticket listing).
  - `/tickets` = REAL/CANONICAL. `GET /api/v1/tenants/{tenant_id}/tickets` — gated by `ticket.read`, canonical `internal/tickets/domain`/`tickets` table (tenant_id + RLS + FORCE RLS, migration `000014`, already used by Inbox/TicketPanel), tenant-scoped via `platformdb.QuerierFromContext` (never a raw pool query), cursor pagination `(updated_at DESC, id DESC)` (same pattern as `contacts`'s `ListContacts`), optional `status`/`priority` filters. No new Ticket model, no SLA, no account linkage, no assignment/resolution capability added — read/list only.
  - **Frozen**: canonical Ticket remains the existing domain; `bpo.Ticket` must never be resurrected (ADR-0011). Future ticket capabilities (assignment, resolution, SLA, account linkage) are separate, not-yet-started slices.
  - **Navigation**: Tickets restored to desktop Sidebar (no longer `mockBacked`). MobileNav unchanged (Conversas/Contatos/Canais only).
  - **Current frontend reality**: REAL/CANONICAL — `/inbox`, `/contacts`, `/channels`, `/supervisor`, `/tickets`, `/settings/team`, `/settings/agents`, `/settings/roles`. MOCK-CONTAINED — `/`, `/reports`, `/accounts`. NOT IMPLEMENTED — Automations.
  - **Validation**: tickets backend integration tests 9/9 PASS (real Postgres, adversarial tenant isolation proven), tenancy + IAM permission matrices PASS, `go build`/`go vet` PASS, tsc PASS, Vitest 138/138 PASS, Vite build PASS, tickets E2E 3/3 + ticket-panel E2E 2/2, full canonical Playwright 33/33 PASS (2 independent clean runs), responsive 1440/1024/390 PASS, `git diff --check` PASS.
  - **Known test debt (not a PRODUCT.2 regression)**: `go test ./...` under the manual throwaway-DB harness still shows pre-existing failures in `internal/inbox/adapters` (`external_id` vs the real column `external_number_id`, plus dependent media-retrieval tests) — reproduced identically with the PRODUCT.2 diff fully stashed.
  - **Next**: `PRODUCT.3 — Analytics Reality / Prioritization Gate` — re-evaluate Dashboard/Reports now that Inbox/Contacts/Channels/Supervisor/Tickets are all real; resolve `internal/reports`'s actual wiring/status before any report decision. Accounts stays out of scope until its domain semantics are decided (ADR-0011). Not started.

- **PRODUCT.2-A = DONE** (2026-09-23). Ticket Read Permission Foundation (`e122a34` feat(iam): add tenant ticket read permission).
  - **Blocker found and resolved**: `PRODUCT.2 — Canonical Ticket Listing` was STOPPED because no `ticket.*` permission existed anywhere (RBAC/IAM3/migrations) — `conversation.claim`/`conversation.manage` govern acting on one conversation, not a tenant-wide aggregate view; `agent.read` governs the agent roster, not ticket content. Neither is a safe substitute.
  - **New permission**: `ticket.read` — tenant-wide read/list visibility of canonical tickets inside explicit `TenantContext`. Does NOT grant conversation claim, reply/send, ticket update/assignment/resolution, cross-tenant visibility, or RLS bypass. Grants: `tenant_admin` YES, `tenant_supervisor` YES, `tenant_agent` NO (same seed pattern as `000038`'s `agent.read`/`agent.manage`). No other `ticket.*` permission introduced.
  - **Migration**: `000043_ticket_read_permission` — permission + role_permissions rows only. Ticket schema: UNCHANGED. Ticket RLS: UNCHANGED. No index added.
  - **Validation**: migrations 000001..000043 apply from zero (verified twice on throwaway DBs), direct SQL confirms exact grants, `TestSystemRolePermissionMatrix`/`TestManageDoesNotImplyOtherManage` PASS against real Postgres, `go build ./...` PASS, `go vet ./...` PASS, `git diff --check` PASS.
  - **Known test-harness caveat (not a regression)**: `go test ./...` under the manual throwaway-DB harness used for this validation shows pre-existing RLS failures in `internal/tenancy/adapters` and `internal/platform/authn` (`new row violates row-level security policy` on `users`/`tenants`) — reproduced identically on an equivalent throwaway DB **without** migration `000043`, so this is a harness artifact (missing bootstrapping step the official `scripts/e2e-inbox.sh`/CI path has), not caused by this change.
  - **Next**: `PRODUCT.2-B — Canonical Ticket Listing` — add the tenant-scoped `GET /tickets`-style list endpoint requiring `ticket.read`, wire `/tickets` to real data, remove it from mock containment. No new ticket model, no new permission, no new migration expected. Not started.

- **PRODUCT.1 = DONE** (2026-09-23). Real Supervisor Presence Overview (`d3e8add` feat(web): add real supervisor presence overview).
  - `/supervisor` = REAL/CANONICAL. Real sources only: agents roster (`GET .../agents`), Valkey presence snapshot (`GET .../agents/presence`), presence SSE (`GET .../agents/presence/events`) — the same ones `AgentsPage.tsx` already consumes. Provides: total/online/offline agent counts, real roster, ONLINE/OFFLINE state, realtime SSE transitions. Canonical invariant preserved: Valkey = realtime presence truth, Postgres `last_seen_at` is never consulted.
  - **Explicitly NOT implemented** (require later real capabilities): queue load, unassigned-conversation metrics, ticket metrics, SLA, productivity/performance metrics, account health, response-time metrics.
  - **Navigation**: desktop Sidebar's "Supervisor" item no longer `mockBacked`, visible outside dev again. MobileNav unchanged (Conversas/Contatos/Canais only).
  - **Current frontend reality**: REAL/CANONICAL — `/inbox`, `/contacts`, `/channels`, `/supervisor`, `/settings/team`, `/settings/agents`, `/settings/roles`. MOCK-CONTAINED — `/`, `/tickets`, `/reports`, `/accounts`. NOT IMPLEMENTED — Automations.
  - **Validation**: tsc PASS, Vitest 136/136 PASS, Vite build PASS, Supervisor/Agents/Presence E2E 2/2 each, full canonical Playwright 30/30 PASS (2 independent clean runs), responsive 1440/1024/390 PASS, `git diff --check` PASS. Backend delta: NONE.
  - **Next**: `PRODUCT.2 — Canonical Ticket Listing` — the canonical `internal/tickets` domain and `tickets` table (RLS + FORCE RLS, tenant-scoped, conversation-linked) already exist and are live; the only missing piece is a cross-conversation tenant-scoped list endpoint + real `/tickets` frontend wiring. Small backend delta expected. Not started.

- **PRODUCT.0 = DONE** (2026-09-23). Real Capability Prioritization Gate — pure investigation, no code changed.
  - **READY**: Supervisor presence/roster slice → READY FOR REAL IMPLEMENTATION, zero backend delta (reuses already-real `GET /agents/presence` Valkey snapshot, `GET /agents/presence/events` SSE, `GET /agents` roster — same sources `AgentsPage.tsx` already consumes). Tickets list → READY AFTER SMALL BACKEND GAP: the canonical `internal/tickets/domain` model and `tickets` table (migration `000014`, RLS+FORCE RLS, tenant-scoped, consumed by real Inbox/TicketPanel) already exist and are live — only a cross-conversation list repository method + `GET /api/v1/tenants/{tenant_id}/tickets` HTTP endpoint are missing.
  - **NOT READY**: Dashboard (needs new cross-domain aggregation semantics, no partial win from existing routes). Accounts (domain semantics unresolved — per ADR-0011, BPO Account must not be conflated with Tenant/CRM Company, and no other concept fills that gap). Reports (`internal/reports` wiring was not exhaustively resolved by this gate — do not claim more than that; depends on Tickets/Dashboard maturing first).
  - **Decision**: `PRODUCT.1 — Real Supervisor Presence Overview` (zero backend delta, canonical presence rule — Valkey is realtime truth, `last_seen_at` in Postgres is not). Second candidate retained for after PRODUCT.1: **Canonical Ticket Listing** — must extend the current canonical ticket model, never resurrect `bpo.Ticket`. Not scheduled yet.

- **BPO.1 = DONE** (2026-09-23). Safe Retirement of `internal/bpo` (`f3d519a` refactor(bpo): remove orphaned legacy domain).
  - `internal/bpo` REMOVED — 32 files deleted, no replacement implementation created. Decision and full reasoning: `docs/adr/0011-retire-orphaned-bpo-domain.md` (Accepted, BPO.0) — not duplicated here. Key future rules from that ADR remain binding: do not resurrect `bpo.Ticket`; do not reuse the raw-pool repository pattern (no RLS/tenant filter); a future Supervisor must align with canonical IAM/presence(Valkey)/routing; BPO Account is not automatically Tenant or CRM Company; `SLAConfiguration`/`AccountType` are concept references only.
  - **Architectural invariants confirmed unchanged**: router, migrations, database schema, frontend, IAM/RBAC, RLS, Inbox/conversation ticket domain, presence/routing, `audit_events` — all untouched by this deletion.
  - **Validation**: `go build ./...` PASS, `go vet ./...` PASS, `go test ./...` PASS (all packages `ok`, zero `FAIL`, `internal/bpo` no longer listed), `git diff --check` PASS. Zero references to `internal/bpo`/`bpo_accounts`/`bpo_tickets` remain outside git history.
  - **Next**: `PRODUCT.0 — Real Capability Prioritization Gate` — decide which currently mock-contained surface (Dashboard, Tickets, Supervisor, Accounts, Reports) becomes real first. Not started.

- **DESIGN.5-A = DONE** (2026-09-23). Channels Legacy Retirement (`7853f89` refactor(web): retire legacy channels integration surface).
  - **Final routes**: `/channels` (canonical), `/channels/whatsapp/new` (canonical WAHA wizard), `/integrations` → compatibility redirect to `/channels`. One Channels frontend implementation, no duplication.
  - **Removed**: `IntegrationsPage.tsx`, `QRPairingModal.tsx`, `IntegrationsPage.test.tsx`, legacy `channels.spec.ts`, and the dead "Integrações" item from `SETTINGS_SECTIONS` (`SettingsShell.tsx`) — its only meaning was the retired page; Sidebar/MobileNav never linked to it. `lib/integrations.ts` (`integrationsAPI`) kept as-is — shared real infra still consumed by canonical Channels, not renamed.
  - **Canonical E2E**: `channels-page.spec.ts` is now the sole Channels product E2E (redirect + non-admin permission + full connect/QR/disconnect/audit flow). The non-admin permission assertion was migrated from the retired legacy spec (same backend-mapped message, different container).
  - **TEST.2-A root-cause finding (fixture isolation, not product bug)**: visiting `/channels` as admin reconciles every visible connection against real WAHA state (`LiveChannelConnectionCard`/`useLiveConnection`) — real product behavior. This demotes the seeded fixture `channel_connection` (`c0000000-...c001`) from `active` to `pending`. `inbox.spec.ts`'s fixture conversation requires that exact connection `active` to accept a reply (`internal/messages/application/send.go`: synchronous `ErrChannelUnavailable` otherwise, no message row inserted). The retired legacy spec restored this in its own `afterAll` — `channels-page.spec.ts` now owns that restoration explicitly, so Inbox's E2E no longer has a hidden dependency on a spec that no longer exists. Diagnosed via a controlled sequence matrix (inbox alone: PASS; canonical channels → inbox with no fix: FAIL, `channel_connections.status` confirmed `pending` before the failure; same sequence with the fix: 3/3 PASS).
  - **Validation**: tsc PASS, Vitest 133/133 PASS, Vite build PASS (398.26kB), canonical Channels Playwright 3/3 PASS, full canonical Playwright 28/28 PASS (2 independent clean runs), `git diff --check` PASS. Backend delta: NONE.
  - **Current frontend reality**: REAL/canonical — `/inbox`, `/contacts`, `/channels`, `/settings/team`, `/settings/agents`, `/settings/roles`. MOCK-CONTAINED — `/`, `/tickets`, `/reports`, `/supervisor`, `/accounts`. NOT IMPLEMENTED — Automations. LEGACY CHANNELS DUPLICATION — none (`/integrations` is a redirect only).
  - **Next**: `BPO.0 — Orphaned BPO Domain Fate Gate` — `internal/bpo` (Account/Ticket/Supervisor/SLA/Audit) is implemented, tested, never wired to the HTTP router, never imported elsewhere; likely root cause behind Accounts/Supervisor being mock-backed. Decide REVIVE+ADAPT / PARTIAL REUSE / ARCHIVE-REMOVE / REPLACE — not started.

- **FRONTEND.2 = DONE** (2026-09-23). Mobile Navigation Real-Surface Alignment (`549f8ef` fix(web): align mobile navigation with real surfaces).
  - **Canonical mobile operational navigation**: Conversas → `/inbox`, Contatos → `/contacts`, Canais → `/channels` — identical in dev and non-dev (none of these three are mock-backed, so no `isDevSurface`/`mockBacked` filtering logic is needed). The old "Mais" item was removed — confirmed pure inert placeholder (`aria-disabled`, no real interaction, deferred to FR10). Dashboard/Tickets were not reintroduced (still mock-backed outside development).
  - **Active state**: `/inbox` → Conversas; `/contacts` and `/contacts/:contactId` → Contatos; `/channels` and `/channels/*` → Canais (uniform `startsWith` check, safe since no remaining item is root `/`). Accessibility preserved: semantic `<Link>`s, `aria-current="page"`, visible labels, `min-h-[44px]` touch targets.
  - **Validation**: targeted `MobileNav.test.tsx` 5/5 PASS, full Vitest 139/139 PASS, tsc PASS, Vite build PASS, responsive proof 390/1024/1440 PASS (real browser: no overflow, correct active state, `main` `padding-bottom: 64px` matches nav height exactly, desktop/tablet shell unchanged), full canonical Playwright 28/28 PASS, `git diff --check` PASS. Backend delta: NONE.
  - **TEST.1 = DONE** (2026-09-23, `21c53e3` test(web): align e2e login helpers with inbox landing) — baseline repair discovered while running FRONTEND.2's required Playwright gate. Root cause: FRONTEND.1 moved the default post-login landing from `/` to `/inbox`, but 10 canonical specs' local login helpers still asserted `waitForURL('/')`, breaking the whole suite before any feature was exercised. Fixed by updating only that assertion in `agents.spec.ts`, `channels.spec.ts`, `channels-page.spec.ts`, `contacts.spec.ts`, `inbox.spec.ts`, `invitation-email.spec.ts`, `presence.spec.ts`, `responsive-smoke.spec.ts`, `roles-permissions.spec.ts`, `ticket-panel.spec.ts` — no new abstraction, no product-code change. `auth.spec.ts`/`dashboard.spec.ts` intentionally left untouched: not in the canonical `testMatch`, test a legacy password-based login UI that no longer exists. Committed separately from FRONTEND.2.
  - **DESIGN.5 reality audit findings (retained)**: `/` (Dashboard), `/tickets`, `/reports`, `/supervisor`, `/accounts` are 100% frontend-fixture-backed (`lib/api.ts` mocks + `dashboardRepository.fixtureRepository`) — no real backend endpoint behind any of them. `/integrations` = legacy duplicate of `/channels`, retirement pending (not performed). Automations = not implemented (no route/page/backend). `internal/bpo` = a fully-built, tested backend domain (Account/Ticket/Supervisor/SLA/Audit) never wired to the HTTP router, never imported outside itself — the likely root cause behind Accounts/Supervisor being mock-backed; its fate (revive-and-wire vs. formally deprecate) is a separate architectural decision, not resolved here.
  - **Current frontend reality (concise)**: REAL operational — `/inbox`, `/contacts`, `/channels`. REAL admin/settings — `/settings/team`, `/settings/agents`, `/settings/roles`. LEGACY — `/integrations`. MOCK-CONTAINED — `/`, `/tickets`, `/reports`, `/supervisor`, `/accounts`. NOT IMPLEMENTED — Automations.
  - **Next candidates queued (not started)**: `DESIGN.5-A — Channels Legacy Retirement` (`/integrations` fate) → `BPO.0 — Orphaned BPO Domain Fate Gate` (revive-and-wire vs. deprecate).

- **DESIGN.4 = DONE** (2026-09-23). Canonical Channels migration.
  - **Canonical frontend**: `/channels` (`ChannelsPage.tsx` + `features/channels/*` + `WahaWizardPage.tsx`). **Legacy temporary**: `/integrations` (`IntegrationsPage.tsx` + `QRPairingModal.tsx`) — kept intact, not retired, not redesigned.
  - **DESIGN.4-0 discovery**: two fully separate frontend implementations of the same channel domain existed. `web/e2e/channels.spec.ts` (pre-existing) only ever tested `/integrations`, never `/channels` — its own comment admitted this ("the sidebar now links to the rebuilt /channels page; this spec still covers the legacy IntegrationsPage shell"). Building a new dedicated E2E for `/channels` exposed a real functional gap before any design work started.
  - **DESIGN.4-0.1** (`305daa7` fix(web): reconcile live state on canonical channels page) — root cause: `WahaConnectionService.List()` (backend) never reconciles live WAHA session state, only `Get(id)` does (`internal/channels/application/waha_connections.go`); the legacy page compensated with a per-card `GET`, the canonical page didn't, so a connection awaiting QR rendered as "Desconectado" and the "Detalhes" resume action never appeared. Fixed entirely in the frontend by wrapping each list row with the existing `useLiveConnection` hook (already shipped for the wizard, `features/channels/data/useChannelSession.ts`) — no new polling loop, no backend change, `WahaConnectionService.List`/`Get` untouched. `list()` remains the source of "which connections exist"; per-row `GET` only refines live state; `toConnectionState` stays server-authoritative. Added `web/e2e/channels-page.spec.ts` — a real E2E for the canonical route (create + risk ack → real WAHA QR → HMAC webhook check → return to `/channels` → card correctly shows "Aguardando QR" with a working "Detalhes" resume → real disconnect → real audit events), same real backend/Postgres/WAHA fixture as the legacy spec, no mocked QR. Integrated into the canonical Playwright runner.
  - **DESIGN.4-A** (`2a30ca4` feat(web): align canonical channels action hierarchy) — the canonical `/channels` UI was audited and found already substantially conformant with the design foundation (existing primitives, semantic composition, restrained cards, real-data-only status). Only one justified adaptation: the `qr_required` "Detalhes" resume action — the single most contextually relevant action, previously a discreet text link — promoted to the existing `Button` primary primitive. No new primitive, no behavior change, no provider change.
  - **Provider reality**: WAHA (unofficial, session/QR-based) is the only implemented provider. Meta Cloud's descriptor already existed pre-session in the backend and is already correctly shown *disabled* with a real `unavailable_reason` in `AddChannelDialog` — not implemented, not added, not touched.
  - **Validation**: `tsc`, Vitest 125/125, Vite build, canonical `channels-page.spec.ts` PASS, legacy `channels.spec.ts` PASS (still independent, cleanup-isolated so run order never breaks either), full Playwright 28/28, responsive 1440/1024/390 proven against the real running app (list + wizard), `git diff --check` — all PASS. Backend delta: NONE across both slices.
  - **E2E proof scope (honest)**: canonical `/channels` E2E proves creation, risk acknowledgement, real WAHA QR, HMAC webhook behavior, live-state reconciliation, `qr_required` correctly reflected, resume via "Detalhes", and the supported disconnect/audit lifecycle — within the same deterministic fixture limits as the legacy test (no real physical WhatsApp device pairing is exercised by either).
  - **Follow-ups**: (A) `CHANNELS LEGACY RETIREMENT` — decide the fate of `/integrations` (any legitimate non-channel responsibility, route/IA, duplicate QR/wizard removal, legacy E2E migration/removal, compatibility strategy) — not performed this session, non-blocking. (B) Live per-card polling (2s, reused from the wizard) is acceptable at current low channel cardinality per tenant — review only if that grows materially, do not optimize speculatively. (C, inherited) Accounts/Integrations permission visibility still lacks a canonical effective-permission mapping.
  - **Next recommended step**: **DESIGN.5 — Frontend Reality / Legacy Consolidation Gate** (not a visual migration) — audit remaining frontend surfaces (Tickets, SupervisorDashboard, Accounts, Automations, `/integrations` legacy) and classify each as REAL+READY FOR DESIGN / REAL+ALREADY CONFORMANT / LEGACY-DUPLICATE / MOCK-BACKED / NOT IMPLEMENTED, before choosing the next design target. Not started this session.

- **DESIGN.3 = DONE** (2026-09-23). Contacts/CRM foundation.
  - **DESIGN.3-A** (`a608913` feat(web): migrate contacts list to design foundation): `/contacts` (ContactsPage) migrated to shared `Table` + cursor `Pagination` — first non-settings consumer, exact contract match (`has_more`/`next_cursor`/`count`, no fabricated page numbers). FilterBar skipped (no search/filter existed). Mobile cards and detail navigation preserved untouched.
  - **DESIGN.3-B** (`c71141a` test(web): add contacts detail E2E coverage) — **VALIDATED / NO PRODUCT-CODE CHANGE**. `ContactDetailPage` was audited and found already conformant with the design foundation: existing primitives, semantic `dl/dt/dd` metadata, local composition, single-column responsive, real data only, no speculative `DetailPageShell`, no CRM 360 fabrication. **Principle**: do not force a visual rewrite when a surface already conforms — REUSE → EXTEND → CREATE means recognizing "already done" as a valid outcome, not just "reuse a primitive." Added dedicated `contacts.spec.ts` (list → detail → back, real fixture data) and integrated it into the canonical Playwright runner (`playwright.inbox.config.ts` testMatch).
  - **Validation**: `tsc`, Vitest 125/125, Vite build, `contacts.spec.ts` 2/2, full Playwright 27/27 (25 pre-existing + 2 new), responsive 1440/1024/390 proven against the real running app, `git diff --check` — all PASS. Backend delta: NONE.
  - **Follow-ups**: (A, inherited) Accounts/Integrations still lack a canonical effective-permission mapping. (B, new, non-blocking) Contacts cursor/back state is lost when returning from `/contacts/:id` — `cursorStack` is component-local state with no persistence; pre-existing behavior, not a DESIGN.3 regression, not tested deterministically yet (current fixture has only 2 contacts, `has_more` never true).
  - **Next wave candidates inspected**: `Tickets.tsx` and `SupervisorDashboard.tsx` both consume `lib/api.ts`'s mock `ticketsAPI`/`dashboardAPI`/`accountsAPI` — same class of problem that excludes Accounts, **not qualified** for a "real" migration yet. `ChannelsPage.tsx` uses the real `integrationsAPI` (same client as `IntegrationsPage`), already has real E2E coverage (`channels.spec.ts`, real QR pairing). Automations has no implementation at all (no route, no file) — not a candidate.
  - **Next recommended wave (at the time)**: DESIGN.4 — Channels migration. Completed — see DESIGN.4 entry above. (Note: the "already E2E-tested" assumption at the time was later found to only cover the legacy `/integrations` page, not `/channels` — see DESIGN.4-0 discovery above.)

- **DESIGN.2 = DONE** (2026-09-22). Second real page migration onto the DESIGN.1 foundation: `/settings/agents` (AgentsPage) — proves the settings design system reuses on a second real administrative surface (`a42485b` feat(web): migrate agent settings to design foundation).
  - **Migrated**: `SettingsShell` (same 4-section admin nav) + `Table`/`TableRow`/`TableCell` for desktop/tablet, new `AgentCard` for mobile (this page previously had zero mobile fallback — just a horizontally scrolling table). Old breadcrumb (Equipe e acesso → Agentes) dropped as redundant now that the shell's own nav shows the active section.
  - **Invariants preserved, none renegotiated**: operational agent source of truth stays the canonical `GET /tenants/{tenant_id}/agents` (`lib/agents.ts`) — legacy `/users/agents` is NOT the operational directory and was never touched. Presence (online/offline) shown in the table/cards is real Valkey snapshot + SSE aggregated transitions (ADR-0010 §11-12) — `agent_profiles.last_seen_at` is NOT presence source of truth and is still not displayed. Queue `available`/`capacity` remain strictly per-queue, never flattened into a single agent-level value. Full queue management modal (toggle eligibility, edit capacity, add/remove) and `agent.read`/`agent.manage` gating unchanged.
  - **Skipped intentionally** (not gaps, correct calls given current product boundaries): FilterBar (no search/filter existed on this page — not added speculatively), Pagination (`agentsAPI.list()` has no cursor/limit contract), IAM4.3 skills (still deferred, no backend consumer), Radix (not needed).
  - **Validation**: `tsc`, Vitest 125/125, Vite build, `agents.spec.ts` 2/2 (full queue-management flow + admin/supervisor/agent permission matrix, real stack), full Playwright 25/25, responsive 1440/1024/390 proven against the real running app (real login, real browser), `git diff --check` — all PASS. Backend delta: NONE.
  - **Next recommended wave (at the time)**: DESIGN.3 — Contacts/CRM foundation. Completed — see DESIGN.3 entry above.

- **DESIGN.1 = DONE** (2026-09-22). Systemic frontend redesign, phase 1: foundation consolidation + first real page migration.
  - **Foundation**: the existing App Shell and design system (`936f4be feat(web): establish Omnira design system and app shell`) were audited against `docs/reference-kits/omnira-ui-design/` and found substantially aligned — preserved and extended, not rebuilt. Lovable (OmniFlow Hub project) used once as design donor for gaps only (dashboard/settings/detail composition patterns, overlays, data primitives, interaction states matrix); the generated monolith was not ported — only specific concepts were adapted/selectively ported after REUSE → EXTEND → CREATE classification.
  - **DESIGN.1-A** (`f61ed17` feat(web): add shared operational UI primitives): `Table`, `FilterBar`, `Pagination` — extracted from patterns already duplicated ad-hoc across ContactsPage/TeamPage/AgentsPage, not speculative. Skipped intentionally: `MetricCard` (already exists in `features/dashboard/`, no real consolidation target), `SegmentedControl` (only real usage is inside the Inbox, protected from changes).
  - **DESIGN.1-B** (`b9383c2` feat(web): add settings layout foundation): `SettingsShell` — real routes (`SETTINGS_SECTIONS`: Equipe e acesso, Agentes, Contas, Integrações), single breakpoint responsive (nav collapses to horizontal scroll below `md`), 44px touch targets (`min-h-[44px]`, same convention as `MobileNav.tsx`). No Radix dependency needed — native `<select>` remained sufficient.
  - **DESIGN.1-C** (`50849ff` feat(web): migrate team settings to design foundation): first real page migration, `/settings/team` (TeamPage) composed onto `SettingsShell` + `FilterBar` + `Table`. Zero regression (23 pre-existing TeamPage tests unchanged + `invitation-email.spec.ts` + `roles-permissions.spec.ts` real-stack E2E). Pagination skipped (contract has no cursor, loads full list — not fabricated). Responsive proven against the real running app (Playwright, real login) at 1440/1024/390.
  - **Validation across the wave**: `tsc`, Vitest 125/125, Vite build, full Playwright 25/25 (`scripts/e2e-inbox.sh`), `git diff --check` — all PASS at every slice. Backend delta: NONE.
  - **Design policy going forward**: REUSE → EXTEND → CREATE (audit existing primitives/tokens before building anything new). Lovable remains design donor only, never architecture/backend/auth authority. Radix approved selectively, only when a real accessibility-heavy consumer requires it (Select/Popover/Dialog/Tooltip candidates) — never adopted speculatively, never shadcn visual styles.
  - **Follow-up (not resolved, not new)**: `Accounts.tsx`/`IntegrationsPage.tsx` have no canonical effective-permission (`useAccess()`) mapping — visibility preserved as-is in `SettingsShell`, not gated. `Accounts.tsx` still uses a pre-existing mock `accountsAPI` (unrelated to DESIGN.1, not touched).
  - **Next recommended wave (at the time)**: DESIGN.2 — migrate `/settings/agents` onto the same foundation. Completed — see DESIGN.2 entry above.

- **RELEASE.1 = CLOSED** (2026-09-22). PILOT READY = YES. PRODUCTION ACTIVATION = NO (requires separate human authorization).
  - Gate covered: operator flow, session/tenant, realtime, assignment, media, contact/ticket, responsive, observability, security sanity — all PASS.
  - Only blocker found (RELEASE.1-B1): `omnira_session` carried a full identity JWT instead of an opaque session id; masked a pre-existing production bug where OIDC login's own opaque cookie was never actually validated by the wired middleware. Resolved in `13274ea` fix(auth): use opaque server-side web sessions — opaque 256-bit session id, `PostgresSessionStore`/`auth_sessions` (reused, no new migration), `authn.WebMiddleware` (cookie → `ResolveSession`, no JWT fallback; Bearer → JWT verify, preserves dev/API consumers), dev login and OIDC login converge on the same session store, logout revokes server-side, production `Secure` cookie enforcement confirmed fail-closed (test added).
  - Gates: `go test ./...`, `go vet ./...`, API/worker build (via `scripts/e2e-inbox.sh`), full Playwright 25/25, `git diff --check` — all PASS.

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
