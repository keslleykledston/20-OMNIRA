# IAM3 — Inventário de enforcement (2026-09-22)

Modelo MVP: **papéis fixos da plataforma** `tenant_admin`, `tenant_supervisor`, `tenant_agent` (atribuíveis por `membership.manage`).
`system_admin` (bypass por GUC `app.is_system_admin`, sem permissões no banco) e `hub_admin` (só `hub.*`) nunca aparecem na administração do tenant.
**Sem CRUD de roles** (custom roles = DEFERRED). Autorização de runtime = chave exata em `role_permissions` do papel da membership **ativa**; sem wildcard, prefixo ou herança entre chaves.

## Matriz role → permissões (fonte: migrations 000002/000023/000024/000036; travada em `internal/iam3/security_test.go`)
| Permissão | admin | supervisor | agent |
|---|---|---|---|
| tenant.read | ✓ | ✓ | ✓ |
| tenant.manage | ✓ | — | — |
| membership.read | ✓ | ✓ | — |
| membership.manage | ✓ | — | — |
| audit.read | ✓ | ✓ | — |
| conversation.claim | ✓ | ✓ | ✓ |
| conversation.manage | ✓ | ✓ | — |
| channel.manage | ✓ | — | — |

## Rotas × permissão
| Rota | Permissão | Classe |
|---|---|---|
| GET/PATCH `/team`, GET `/roles` | membership.read / membership.manage / membership.read | ENFORCED |
| `/team/invitations` (POST/GET/PATCH) | membership.manage / read / manage | ENFORCED |
| GET `/me/access` | própria membership (self) | ENFORCED |
| GET `/audit` | audit.read | ENFORCED |
| GET `/users/agents` | **conversation.manage** (corrigido no IAM3; era sem checagem). **TEMPORARY SEMANTIC COUPLING**: único consumidor = seletor de convidar/transferir co-atendente (`TechnicianSelectModal`), operações que já exigem `conversation.manage`; revisar quando Agent/Queue permissions forem consolidadas (sem `agent.read` por ora) | ENFORCED |
| POST assign/unassign, invite, transfer, messages | conversation.claim / conversation.manage | ENFORCED |
| accept-invite / reject-invite / leave | escopo à própria participação (RLS) | PARTIAL (sem gap provado) |
| `/channels/**` (waha e genérico) | channel.manage | ENFORCED |
| GET `/tenants/{id}` | só membership ativa (tenant.read não checada) | PARTIAL |
| Inbox list/get/messages/SSE, contacts | só membership ativa + RLS; **não existe permission de leitura no catálogo** | UNENFORCED por desenho (sem chave `*.read`; não criar sem decisão de produto) |
| Tickets/CRM (`/ticket*`, `/crm/activity`, `/integrations/companies`) | nenhuma | UNENFORCED — **RESERVED/DEFERRED FOR FR4** (`ticket.read/create/update/assign/resolve`; sem `ticket.manage`) |
| ~~`/members` legado (GET/POST/DELETE)~~ | — | **REMOVIDO no IAM3**: sem checagem e `role_id` irrestrito permitia a um tenant_admin conceder `system_admin`/`hub_admin` |

Catálogo: `tenant.read/manage` só concedidos (CATALOG-ONLY), `hub.*`/`grant.*` CATALOG-ONLY (sem rotas de hub).
Revogação: `AuthorizeAccessToTenant` só aceita membership `active` e todo SQL de permissão exige `m.status='active'`.

## Mudanças do IAM3 (esta wave)
- `GET /roles` passou a devolver `permissions[]` por papel (aditivo; `membership.read`); documentado com `GET /me/access` no OpenAPI.
- Rotas legadas `/members` removidas; `/users/agents` exige `conversation.manage`.
- `Role.HasPermission` de domínio (código morto) deixou de conceder `admin` cross-resource.
- Front: `useAccess().can()` único (react-query, com token) em vez de checagem por nome de papel; tela read-only "Funções e permissões".
- Testes: matriz de papéis, `/roles` com permissões, `/users/agents`, ausência de rotas de edição de role/`/members`, vitest da tela, Playwright `roles-permissions.spec.ts`.

## Verificação de consumidores (2026-09-22)
- `/users/agents`: só `TechnicianSelectModal` (usado por `ConversationPage`, hoje sem rota). Supervisor tem `conversation.manage`; `tenant_agent` não invita/transfere no backend de qualquer forma.
- `/members` legado: zero consumidores (frontend, scripts, tools, OpenAPI). Restam handlers **sem rota** `ListMemberships/CreateMembership/RevokeMembership` em `tenancy/adapters/http_handlers.go` e 3 testes unitários que os chamam direto — candidatos a remoção; o teste `TestNoRoleEditingOrLegacyMembersRoutes` impede o re-registro das rotas.
