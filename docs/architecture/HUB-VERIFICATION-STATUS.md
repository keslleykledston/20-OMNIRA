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
| Projetor da inbox (`internal/worker/hubprojector`) | projection | **POSTGRES VERIFIED** (espelha nome, canal, fila, atribuição, prioridade do chamado, não lidas; idempotente; remove ao revogar/expirar/suspender; ignora conversa interna; isola hub e tenant). 9 mutações do SQL detectadas (`scripts/test-hub-projector-mutations.sh`). Ligado ao worker atrás de `OMNIRA_HUB_PROJECTOR_ENABLED=false`. **NOT DEPLOYED**; gatilho por evento ainda não existe (reconciliação periódica) |
| Provisionamento (hub, membro, contrato, grant) — `internal/hub/provisioning` + `omnira-hubctl` | service + CLI de operador | **POSTGRES VERIFIED** (validações no servidor, auditoria na mesma transação, o que o agente enxerga pela RLS após cada passo) e **CLI executada de ponta a ponta** contra Postgres. 8 mutações detectadas (`scripts/test-hub-provisioning-mutations.sh`). Imagem do API compila com a ferramenta. **Sem API HTTP**: o OMNIRA não tem "administrador de plataforma" humano. **NOT DEPLOYED** |
| Faixa de contexto de tenant no `ChatPane` (`TenantContextBar`, `TenantBadge`, `--tenant-accent`) | frontend | **UNIT VERIFIED** (jsdom): aparece só com 2+ empresas e usa a empresa da sessão; testes falham se ignorar a sessão, aparecer com 1 empresa ou quebrar com empresa desconhecida. Verificada também em Chromium (ver a linha da tela `/hub`) |
| `GET /api/v1/hubs` (meus hubs ativos) e `tenant_name` nas linhas | HTTP slice | **HTTP VERIFIED** (só os meus, ativos, ordenados; nome só de tenant com grant vivo; grant revogado não revela nome). 5 mutantes do handler mortos |
| Tela `/hub` (inbox agregada; assumir/responder só com `can_reply`) + entrada no Sidebar e na barra mobile | frontend | **UNIT VERIFIED** (21 testes jsdom, 10 mutantes mortos) e **verificada em Chromium real** (desktop e 390 px) com **API mockada**, telas revisadas. O teste pegou um bug real (id de item vazando para outro Hub por um render). `MessageBubble` **não** é reutilizado: ele busca mídia pelo tenant da SESSÃO |
| Fiação real: `omnira-api` (login de dev) + `omnira-hubctl` + projetor + Postgres + NATS | integração | **E2E VERIFIED (parcial)** por `scripts/e2e-hub-smoke.sh`, 14 verificações com os binários reais: hubs, inbox só de A e B, nome da empresa, C ausente e item de C = 404, sem hub = 404, anônimo = 401, `tenant_id` forjado = 400, revogação some na leitura seguinte, flag desligada = 404. Falha de verdade se a revogação ou a flag quebrarem (2 mutantes do script mortos). **Não** cobre: navegador real contra o API real, Keycloak/OIDC real, canal real |
| Escrita via Hub: assumir e responder (texto) — `internal/hub/replying`, `messages/application.DelegatedSender`, `POST .../claim` e `.../messages`, capacidade `can_reply` por grant (ADR-0037) | service + HTTP | **POSTGRES VERIFIED + HTTP VERIFIED** (`reply_integration_test.go`: capacidade, claim explícito, exclusividade e corrida de 2 agentes, idempotência, empresa exibida ≠ item (409), IDOR/hub forjado/seletor/campo desconhecido, revogação de grant/contrato/hub/membro/capacidade/escopo de fila, atendimento finalizado, revogação **entre autorizar e gravar**, e a base ainda recusa `UPDATE conversations`/`INSERT messages|outbox_events` direto do agente). **16 mutações detectadas** (`scripts/test-hub-reply-mutations.sh`, inclui a função SQL). Smoke com binários reais cobre claim/reply/revogação. Atrás de `OMNIRA_HUB_API_ENABLED`. **NOT DEPLOYED**. Não cobre: anexos, templates, notas, transferência, entrega real ao canal |

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
| MÉDIO | Funções auxiliares viram oráculo entre usuários | **Reproduzido**. Corrigido nas funções do Hub e, com autorização do dono, também em `has_active_membership`/`has_active_admin_membership` (migration 097; vermelho sem ela, verde com ela) |
| MÉDIO | `service_scope = 'null'::jsonb` lido como irrestrito | **Reproduzido**. Corrigido: `CHECK` de objeto, SQL nega não-objeto, Go nega mapa nulo |
| MÉDIO | `NewTenantContext(..., "")` vira `direct` em silêncio | Corrigido (rejeita). Nenhum chamador passava vazio |
| BAIXO | Respostas de erro sem `Cache-Control: no-store` | Corrigido (`http.Error` do Go remove o cabeçalho; helper próprio) |

O Codex não conseguiu **rodar** testes (sandbox somente leitura) e registrou `CODEX_PLUGIN_NOT_EXECUTED` para a parte de execução; a leitura do código foi feita. Os testes foram executados por mim.

## Limites conhecidos (não resolvidos)
- A RLS do OMNIRA confia que a **aplicação** define as GUCs `app.current_user_id`/`app.is_system_admin`; quem executa SQL arbitrário na sessão da aplicação pode definir `app.is_system_admin`. Pré-existente; o Hub não muda esse modelo.
- `work_pool_id` do grant **não** restringe acesso na RLS. O escopo hoje é por fila no contrato.
- Revogação é imediata no banco, mas **sessões longas** (SSE/WebSocket) abertas antes da revogação dependem do recheck de stream existente; não há stream do Hub e isso **não** foi testado.
- 10 testes de `internal/tenancy/adapters`, 2 de `internal/worker/jobsstream` e, no frontend, 1 teste do `SettingsShell` + 1 spec Playwright capturada pelo vitest já falham no `main`; a branch não os altera.
- O projetor é reconciliação periódica (intervalo configurável), não por evento: uma conversa nova aparece na inbox do Hub só no próximo ciclo.
- `sla_due_at` não é projetado: a plataforma não tem fonte de SLA contratual (o inbox do tenant só tem limiares de exibição).

## Implantação (LAB) — 2026-10-08

- Código `074042c` implantado (api, worker, web). Migrations 093..097 aplicadas ao banco vivo (estava em 092) após `scripts/backup-omnira-db.sh` (dump `omnira_dev_20261008T102318Z`, cópia externa e na nuvem conferidas). Imagens de rollback: `20-omnira-{api,web,worker}:rollback-pre-hub-20261008-0623`.
- Flags ligadas: `OMNIRA_HUB_API_ENABLED=true`, `OMNIRA_HUB_PROJECTOR_ENABLED=true`. **Nenhum Hub provisionado** (`service_hubs` vazio): as rotas respondem 401 sem sessão, o projetor roda sem trabalho. Nenhum usuário tem acesso delegado.
- Provado no ambiente vivo: containers saudáveis, rota `/api/v1/hubs/{id}/inbox` → 401 sem auth, log "Hub inbox projector started", asset do web igual ao `web/dist`. **Não provado no vivo:** sessão real de agente do Hub, entrega real ao canal pelo Hub, navegador contra o API real.
- Rollback: `docker tag` das imagens `rollback-pre-hub-*` para `:latest` + `up -d --no-deps --force-recreate`; desligar as flags no `.env` basta para desativar o Hub (as migrations 093..097 são aditivas e ficam).
- Revisão Codex da fatia de resposta: tarefa `task-muze1tdt-h8zgaz` iniciada, resultado ainda não lido.

## Fase 0 do ADR-0038 (migrations 098/099) — 2026-10-08, NÃO implantada
- **POSTGRES VERIFIED:** empresa `suspended`/`inactive` não é lida nem respondida pelo Hub (assumir/responder → 404, inbox some, volta ao reativar); `platform_operators` invisível a terceiros, sem autopromoção, `is_platform_operator` não é oráculo; 24 mutantes mortos; migrations 093..099 up/down/up idênticos.
- **IMPLEMENTED / NOT WIRED:** `hubctl platform-operator`. Nenhuma rota HTTP usa o papel ainda (fase 1).
- **Lacuna fechada:** até a 097 (a versão implantada) suspender uma empresa NÃO impedia o atendimento pelo Hub.
- **Pendente:** implantar 098/099 (backup + migrate + imagem com o novo hubctl), cadastrar o operador, revisão Codex.

## Fase 1 do ADR-0038 (migration 100, API e tela de Empresas) — 2026-10-08
- **POSTGRES / HTTP VERIFIED:** só operador ativo que administra o Hub gera resposta (todo o resto: 404 uniforme); criar empresa (tenant, fila padrão, contrato, sem acesso a ninguém, auditoria sob o operador, idempotência, rollback total em recusa); suspender/reativar (Hub deixa de servir e volta, grants intactos); capacidades por empresa (padrão ligado, uma empresa por vez, auditoria só da mudança real, membro lê mas não escreve); serviço recusa sozinho quem não é operador-admin.
- **UNIT VERIFIED:** gate de canais (WhatsApp e ERP/CRM têm chaves próprias; provedor não é alcançado quando desligado).
- **Navegador com API MOCKADA** (não é E2E real): `hub-companies.mock.spec.ts`.
- **IMPLEMENTED, NOT TESTED beyond compilation:** gate de anexos e da rota WAHA legada no wiring do servidor.
- **NOT WIRED / não existe:** matriz de atendentes (fase 2), gestão delegada de canais/ERP pelo Hub (fase 3), pools/distribuição (fase 4), E2E com API real (fase 5).
- **HTTP VERIFIED com os binários reais** (`scripts/e2e-hub-smoke.sh`: omnira-api + omnira-hubctl + Postgres + NATS descartáveis, login de desenvolvimento): o operador passa a valer no pedido seguinte, sem reiniciar; criar/replay/chave com outro corpo; suspender tira a empresa do Hub e reativar a devolve; capacidade desligada fica gravada; 4 eventos de auditoria sob o operador; flag desligada = rotas inexistentes e tela não anunciada. Não usa Keycloak, navegador nem canal real.
- **Gate de integração completo (sem argumentos):** 29 pacotes ok; os mesmos 2 vermelhos de antes (`tenancy/adapters`, `worker/jobsstream`; 12 testes, idênticos no `main`). Mutantes: 20 de plano de controle (`test-hub-admin-mutations.sh`) + 24 anteriores, todos mortos. Migrations 093..100 up/down/up idênticas.

## Implantação (LAB) do plano de controle — 2026-10-08
- Código `b330ccd` (api, worker, web) e migrations 098..100 no banco vivo, após backup (`omnira_dev_20261008T185535Z`, cópia externa e na nuvem conferidas). Rollback: imagens `20-omnira-{api,web,worker}:rollback-pre-controlplane-20261008-1455`; `.env` anterior guardado fora do repositório.
- `OMNIRA_HUB_ADMIN_API_ENABLED=true`. Operador de plataforma cadastrado: **um** (o dono), hub_admin do Hub "K3G Solutions".
- Conferido no vivo: containers saudáveis; `GET /api/v1/hubs/{id}/companies` sem sessão → 401 (rota montada); asset do web igual ao `web/dist`; a função de acesso do Hub continua dando acesso de leitura e resposta aos 3 tenants (todos ativos) ao agente provisionado; `is_platform_operator` verdadeiro para o dono; sem erro nos logs.
- **Não conferido no vivo:** a tela `/hub/empresas` com sessão real, criar/suspender de verdade, e o efeito de uma capacidade desligada numa empresa real. Revisão Codex da fase 0+1: `task-muzwcp46-3t8nx2`, resultado ainda não lido.
