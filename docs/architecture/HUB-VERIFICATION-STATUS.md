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
| Schema `service_hubs`, `hub_memberships`, contratos, grants, work pools, skills, inbox (093) | schema | **POSTGRES VERIFIED** (093..096 aplicam, desfazem ao schema exato pré-Hub e reaplicam idêntico: `scripts/test-hub-migrations.sh`). FKs compostas provadas por `TestHubRLS_RelationalIntegrity`. NOT DEPLOYED |
| RLS + funções `SECURITY DEFINER` (094) | policy | **POSTGRES VERIFIED**: `TestHubRLS_*` com 16 mutações detectadas (`scripts/test-hub-rls-mutations.sh`). A inbox é só delegada e autorizada pela fila **atual** da conversa |
| Leitura delegada em `tenants`, `conversations`, `messages` (095) | policy | **POSTGRES VERIFIED**, somente `SELECT`. Escrita via Hub: não existe policy |
| Fixar `pg_temp` por último nas funções `SECURITY DEFINER` existentes (096) | hardening, **independente do Hub** | **POSTGRES VERIFIED** (`TestSecurityDefinerFunctionsPinPgTempLast`). Pode ir ao `main` sozinha |
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

## Revisão adversarial (Codex) — achados e destino
Revisão somente leitura, escopo restrito à autorização do Hub. Cada achado foi **reproduzido por um teste vermelho antes de corrigir**.

| Severidade | Achado | Resultado |
|---|---|---|
| CRÍTICO | Tabelas `TEMP` falsificadas sombreiam tabelas dentro de funções `SECURITY DEFINER` (`search_path = public`) e forjam acesso | **Reproduzido** (2 linhas do tenant C visíveis a um usuário sem hub, grant ou membership). Corrigido nas funções do Hub (nomes qualificados + `pg_temp` por último) e, para o código **pré-existente** (9 funções), na migration 096 |
| ALTO | A inbox confia na `queue_id` da projeção em vez da fila atual da conversa | **Reproduzido**. Corrigido: a linha só é visível se a conversa pai for legível (escopo pela fila real) |
| ALTO | Membro do hub + membro direto do tenant, sem grant, listava a inbox do hub | **Reproduzido**. Corrigido: a inbox do Hub é só delegada |
| MÉDIO | Contrato (`service_scope`, tenant) legível após grant expirado, contrato revogado ou hub suspenso | **Reproduzido**. Corrigido |
| MÉDIO | Funções auxiliares viram oráculo entre usuários | **Reproduzido**. Corrigido nas funções do Hub (exigem o usuário da sessão ou sessão de sistema). `has_active_membership` pré-existente tem o mesmo padrão e **não** foi alterada |
| MÉDIO | `service_scope = 'null'::jsonb` lido como irrestrito | **Reproduzido**. Corrigido: `CHECK` de objeto, SQL nega não-objeto, Go nega mapa nulo |
| MÉDIO | `NewTenantContext(..., "")` vira `direct` em silêncio | Corrigido (rejeita). Nenhum chamador passava vazio |
| BAIXO | Respostas de erro sem `Cache-Control: no-store` | Corrigido (`http.Error` do Go remove o cabeçalho; helper próprio) |

O Codex não conseguiu **rodar** testes (sandbox somente leitura) e registrou `CODEX_PLUGIN_NOT_EXECUTED` para a parte de execução; a leitura do código foi feita. Os testes foram executados por mim.

## Limites conhecidos (não resolvidos)
- **Oráculo pré-existente**: `has_active_membership(tenant, user)` e `has_active_admin_membership` aceitam qualquer usuário como argumento (000004). Não alterei: muda comportamento de código fora do Hub. Recomendo tratar à parte.
- A RLS do OMNIRA confia que a **aplicação** define as GUCs `app.current_user_id`/`app.is_system_admin`; quem executa SQL arbitrário na sessão da aplicação pode definir `app.is_system_admin`. Pré-existente; o Hub não muda esse modelo.
- `work_pool_id` do grant **não** restringe acesso na RLS. O escopo hoje é por fila no contrato.
- Revogação é imediata no banco, mas **sessões longas** (SSE/WebSocket) abertas antes da revogação dependem do recheck de stream existente; não há stream do Hub e isso **não** foi testado.
- 10 testes de `internal/tenancy/adapters` e 2 de `internal/worker/jobsstream` já falham no `main`; a branch não os altera.
