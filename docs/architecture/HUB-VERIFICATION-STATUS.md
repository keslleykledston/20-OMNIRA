# Hub — Verification Status

Vocabulário (a mesma palavra nunca é usada para um nível acima do que foi provado):

| Termo | Significa |
|---|---|
| IMPLEMENTED | código existe |
| NOT WIRED | existe, mas nada o chama em produção |
| UNIT VERIFIED | teste unitário determinístico passa |
| POSTGRES VERIFIED | teste contra PostgreSQL real (papel `omnira_app`, FORCE RLS) passa |
| HTTP VERIFIED | teste HTTP real (handler + middleware + Postgres) passa |
| E2E VERIFIED | fluxo ponta a ponta com navegador/canal real |
| BLOCKED | impedido por pendência explícita |

`go build` e `go vet` são pré-requisitos, **não** aceitação.

Estado desta branch: `fix/integrate-lovable-into-omnira`. **Nada do Hub está implantado**; o banco vivo (`omnira_dev`) está na migration 092.

## Por camada

| Parte | Classe de código | Estado |
|---|---|---|
| Schema `service_hubs`, `hub_memberships`, contratos, grants, work pools, skills, inbox (093) | schema | **POSTGRES VERIFIED** (aplica, desfaz ao schema exato pré-Hub, reaplica idêntico: `scripts/test-hub-migrations.sh`). FKs compostas provadas por `TestHubRLS_RelationalIntegrity`. NOT DEPLOYED |
| RLS + funções `SECURITY DEFINER` (094) | policy | **POSTGRES VERIFIED**: `TestHubRLS_*` com 12 mutações da policy detectadas (`scripts/test-hub-rls-mutations.sh`) |
| Leitura delegada em `tenants`, `conversations`, `messages` (095) | policy | **POSTGRES VERIFIED**, somente `SELECT`. Escrita via Hub: não existe policy |
| `domain` (`internal/hub/domain`) | domain model | IMPLEMENTED. Usado pelo adapter e pelo serviço |
| `ports.HubRepository` | port | IMPLEMENTED. Interface larga demais (veja *dead abstraction*) |
| `adapters.PostgresHubRepository` (6 métodos usados) | repository | **POSTGRES VERIFIED** via `TestHubAuthorizationAgreesWithRLS` e HTTP |
| `application.HubAuthorizationService` | authorization primitive | **UNIT VERIFIED** + **POSTGRES VERIFIED** (concorda com a RLS em 15 estados) |
| `application.EffectiveAccessResolver` | authorization service | **UNIT VERIFIED**. Fonte obrigatória e explícita, sem fallback direct↔hub |
| `domain.TenantContext` (Hub) | context | **UNIT VERIFIED**. `NewTenantContext` recusa `hub` e fontes desconhecidas |
| `adapters.HTTPHandler` (`GET /hubs/{id}/inbox`, `.../{item}`) | HTTP slice | **HTTP VERIFIED** (negativos: IDOR, seletor forjado, revogação imediata, escopo de fila, paginação). Atrás de `OMNIRA_HUB_API_ENABLED=false` |
| Projeção da inbox (`hub_inbox_items`) | projection | **NOT WIRED**: tabela e leitura existem; **não há projetor** que a preencha a partir das conversas |
| Provisionamento (criar hub, contrato, grant) | service | **NOT BUILT**. Hoje só por SQL do dono |
| UI do Hub | frontend | **NOT BUILT** (frontend congelado) |
| Escrita via Hub (responder, atribuir) | service | **NOT BUILT**. Todos os caminhos de escrita existentes exigem `Source==direct`, então um contexto Hub é recusado por construção |

## Dead abstraction (candidatos a remoção no próximo corte)
`PostgresHubRepository` expõe ~30 métodos; só 6 têm consumidor (`GetServiceHubByID`, `GetHubMembership`, `GetEffectiveGrant`,
`GetServiceContract`, `ListHubInboxItems`, `GetHubInboxItemByID`). Os de `WorkPool`/`Skill`/`AgentSkill` retornam `"not yet implemented"`.
Os demais `Create*/List*/Upsert*/Delete*` não têm chamador. Regra: **não crescer o domínio antes de fechar um corte vertical real**; remover
ou implementar com consumidor + teste.

## Provado nesta etapa (as três afirmações exigidas)
1. **Acesso delegado do Hub funciona no PostgreSQL real**: Alice (grant em A e B) lê A e B; Bob só B (`RLS-003/004/006`).
2. **Acesso cruzado indevido é bloqueado no PostgreSQL real**: Alice não lê C; membership de Hub sem grant não dá nada; grant expirado, ainda não iniciado,
   revogado ou suspenso, contrato revogado/suspenso/expirado, hub suspenso e saída do hub negam **imediatamente** (`RLS-005/007/008/009/010`).
   Escopo de fila: lista permitida, lista vazia, valor malformado e fila de outro tenant (`RLS-011`).
3. **A autorização da aplicação estabelece o `EffectiveTenantContext` sem atalho de system-admin** (`RLS-012`): todos os testes rodam com
   `isSystemAdmin=false` e o papel de aplicação não tem `BYPASSRLS`.

## Limites conhecidos (não resolvidos)
- A RLS do OMNIRA confia que a **aplicação** define as GUCs `app.current_user_id`/`app.is_system_admin`. Isso é pré-existente; o Hub não muda esse modelo.
- `work_pool_id` do grant **não** restringe acesso na RLS (só na semântica futura de roteamento). O escopo hoje é por fila no contrato.
- Revogação é imediata no banco, mas **sessões longas** (SSE/WebSocket) abertas antes da revogação precisam ser reavaliadas pelo recheck de stream do
  inbox existente; isso **não** foi testado para o Hub (não há stream do Hub).
- 10 testes de `internal/tenancy/adapters` já falham no `main` (aceite de convite, matriz de papéis); a branch não os altera.
