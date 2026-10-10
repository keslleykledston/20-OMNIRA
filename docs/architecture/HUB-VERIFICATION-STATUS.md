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

## Painel de Acessos, regra de instância única e Conversas unificadas (ADR-0039, migration 101) — 2026-10-08
Estado: **LAB, implantado em 2026-10-09 (migration 101 aplicada, `OMNIRA_HUB_ACCESS_API_ENABLED=true`, imagens `sha-9f7cb5a`)**, atrás de `OMNIRA_HUB_ACCESS_API_ENABLED` (API) e `OMNIRA_HUB_API_ENABLED` (Conversas unificadas). Revisão Codex desta fatia: `task-mv058iab-uvy540` (6 HIGH, 2 LOW; todos tratados abaixo, 1 por refutação parcial).
- **POSTGRES / HTTP VERIFIED** (`internal/hub/adapters`, `internal/hub/provisioning`, `internal/tenancy/adapters`, `internal/messages/adapters`): só `hub_admin` ativo do Hub do caminho passa (agente do mesmo Hub, administrador da empresa, admin de **outro** Hub, estranho: 404 uniforme; anônimo 401; nenhuma linha muda em recusa; o serviço recusa sozinho; conta **inativa** também é recusada); a célula da matriz muda o que o banco deixa o agente ler (`none|read|reply`, uma linha por célula, outra empresa continua fechada); adicionar agente não concede nada; e-mail inexistente e inativo são indistinguíveis; `hub_admin` não é criado/rebaixado/removido pelo painel, **nem em corrida com o hubctl** (teste com duas goroutines, 25 rodadas, falhava antes do ajuste); administrador de instância: adicionar, idempotência, o último não sai, rebaixa a agente (não expulsa), empresa de fora do Hub = 404; a pessoa responsável está em `audit_events.actor_id`.
- **Regra "só o admin do Hub libera em mais de uma instância"** (migration 101, POSTGRES): convite recusado (409) para quem tem membership ativa em outra empresa ou é de um Hub (inativa em outro lugar não conta); vale na criação, no reenvio e na **reativação de membership pelo `PATCH /team`** (brecha achada pelo Codex, fechada); "perguntar e escrever" é atômico por pessoa (duas reativações simultâneas em empresas diferentes: exatamente uma passa; teste falhava antes: 2 memberships ativas); as duas funções SQL (por e-mail e por usuário) não são oráculo (colega sem `membership.manage`, quem não é da empresa: sempre `false`). O **aceite** de convite usa o mesmo lock e a mesma pergunta, mas **não tem teste HTTP** (os testes de aceite do pacote já estavam vermelhos no `main`).
- **Último administrador:** `PATCH /team` e o painel serializam por empresa (lock consultivo comum); duas desmoções simultâneas de administradores diferentes deixavam a empresa sem nenhum (teste falhava antes no round 0).
- **Conversas unificadas** (POSTGRES/HTTP): `?companies=` só estreita (empresa sem grant = vazio; `tenant_id` segue 400; id inválido 400; paginação mantém o filtro); as empresas oferecidas vêm dos grants **vivos e já iniciados** do próprio usuário (revogado ou ainda não iniciado não aparece; um admin do Hub só recebe as que ele mesmo atende); com 2+ Hubs, usa o que serve 2+ empresas.
- **Gate de anexos pela ROTA:** o primeiro desenho tinha a chave no *sender* errado (o handler usava outro): achado do Codex, confirmado, corrigido (`Attachments.SendMedia`); teste de integração pela rota HTTP real com os dois senders distintos, como em produção.
- **Outras correções do Codex:** `grant add` repetido não amplia sem `Renew`/`--renew`; JSON de um só valor; e-mail do primeiro administrador sem oráculo.
- **Mutantes:** 43 mortos em `scripts/test-hub-access-mutations.sh` (painel, guard, regras de grant, locks, filtro, gate de envio, convite, reativação e as duas funções da 101) + `test-hub-admin-mutations.sh` + `test-hub-reply-mutations.sh`, todos verdes no código final. Migrations 093..101 up/down/up idênticas.
- **Real-binary smoke** (`scripts/e2e-hub-smoke.sh`, omnira-api + hubctl + Postgres + NATS descartáveis, login de desenvolvimento): painel 404 para quem não é do Hub; o admin (sem ser operador) lê as 4 instâncias; célula `read` abre exatamente a empresa B e só ela é oferecida ao filtro; agente não se concede nada; tirar a célula fecha B na leitura seguinte; filtro por empresa; flag desligada = painel inexistente. PASS.
- **Gate de integração completo (sem argumentos):** 30 pacotes ok, 1 vermelho (`tenancy/adapters`: os **mesmos 10 testes** que já falhavam no `main` — comparei os nomes um a um; os testes novos desse pacote passam).
- **Vitest:** 755 de 756 (a falha é a do `SettingsShell`, anterior). **Playwright com API MOCKADA** (não é E2E real): matriz, administradores, painel indisponível para não-admin, filtro de empresas no lugar do menu de canais; achou e corrigiu um bug real (o filtro sumia enquanto a consulta estreita carregava; teste de regressão falha sem a correção).
- **Refutado em parte:** "usuário inativo continua como hub_admin" — a autenticação já exige `users.status='active'` a cada pedido (`ResolveUserID`); mesmo assim o painel agora confere na própria transação.
- **NOT VERIFIED:** nada disto foi visto com sessão real do Keycloak nem com canal real; o aceite de convite pela regra nova; o efeito em empresas reais; (migration 101 aplicada no banco vivo em 2026-10-09; telas e rotas respondem como esperado, 401 anônimo em `/api/v1/hubs/{hub}/access`; sem sessão real não foi visto por um administrador).
- **Achados HIGH do Codex de revisões anteriores:** fechados em 2026-10-09, ver a seção "Suspensão de ponta a ponta" abaixo (H-02, H-03, M-01, H1, M1, M3); M-02 (ator do `hubctl`) é risco aceito e documentado no ADR-0038.
- **Achado de infraestrutura:** o disco `/var` chegou a 100% (volumes anônimos de Postgres de testes nunca removidos: 330 volumes, ~70 GB recuperáveis). Os scripts de teste passaram a usar `docker rm -fv`; limpei só o cache de build (regenerável). Os volumes antigos **não** foram apagados: aguardam decisão do dono. Enquanto o disco estava cheio, testes falharam com "No space left on device" — resultados daquela janela foram descartados e refeitos.

## Suspensão de ponta a ponta e corrida do reply (ADR-0038, achados H-02/H-03/M-01/H1/M1/M3) — 2026-10-09
Estado: **LAB, ainda NÃO implantado** (commit local). Sem migration. Detalhe das decisões no ADR-0038, seção "Suspensão de ponta a ponta".
- **POSTGRES VERIFIED:** webhook (Meta ponta a ponta pela rota real) e grupo de empresa suspensa não gravam nada, nem o registro de deduplicação, e respondem 200; reativada, o mesmo evento é gravado. Entrega: mensagem enfileirada antes da suspensão vira `failed/company_suspended` sem chamar o provedor. Fluxos: o mesmo job começa o fluxo só depois da reativação; timeout vencido durante a suspensão dispara só depois. Projetor: congelado e atualizado ao reativar. `LockTenantActive`: a suspensão **espera** quem perguntou primeiro.
- **Corrida do reply (H1), determinística:** uma transação segura a mudança (`UPDATE` não confirmado) de grant, contrato, empresa, Hub ou vínculo; o claim/reply HTTP **não termina** enquanto ela está aberta e, ao confirmar, recusa (404) ou segue (vínculo tocado sem mudar nada → 200/202). Mais: conversa finalizada em andamento → 409.
- **Mutantes:** 15 mortos em `scripts/test-hub-suspension-mutations.sh` (5 travas do reply, finalização, auditoria do tipo de recurso, trava e filtro do helper, entrada de webhook, entrada de grupo, entrega, fluxo, timeout do fluxo em duas camadas, projetor). Um mutante que não compilava foi pego pelo critério "precisa de ao menos um teste falhando".
- **Limite honesto:** o que um cliente escrever com a empresa suspensa **não é guardado** (decisão do ADR-0038: entradas ignoradas). Uma chamada ao provedor que já estava em andamento quando a suspensão confirmou não é desfeita (o lock a faz esperar, não a cancela).
- **NOT VERIFIED:** nada visto com webhook real do WAHA/Meta nem com suspensão feita por um operador de verdade.

## Autorizar pessoa por e-mail (ADR-0039 §3.10, migration 102) — 2026-10-09
Estado: **LAB, ainda NÃO implantado** (commit local), atrás de `OMNIRA_HUB_ACCESS_API_ENABLED`. O gancho do primeiro acesso só é ligado quando `OMNIRA_HUB_API_ENABLED` e `OMNIRA_HUB_ACCESS_API_ENABLED` estão ligadas.
- **POSTGRES / HTTP VERIFIED** (`internal/hub/adapters`, 11 testes `TestAccessInvite_*`, pelo login real `PostgresIdentityResolver.ProvisionIdentity` com o mesmo gancho do servidor): conta existente vira agente na hora e o **banco** passa a deixar ler exatamente as empresas escolhidas (e nada da C); e-mail sem conta fica guardado (14 dias), listado, e pedir de novo substitui; vale no primeiro login com e-mail **verificado** e só uma vez; e-mail não verificado nunca aplica e fica esperando; vencida, cancelada ou de autor que deixou de ser admin (`void`) nunca aplica; empresa suspensa nesse meio-tempo é pulada e as outras valem; seis logins simultâneos aplicam uma vez só; conta inativa é tratada como sem conta; admin de outro Hub e estranho não cancelam; validação (e-mail, modo, empresa repetida, mais de 50, empresa de outro Hub = 404); hub_admin não é rebaixado.
- **Mutantes:** 18 mortos em `scripts/test-hub-invite-mutations.sh` + 1 mutante equivalente documentado (a checagem de admin do handler sozinha sobrevive de propósito: o serviço pergunta de novo ao banco).
- **Migrations 093..102** sobem, descem e sobem de novo idênticas (`scripts/test-hub-migrations.sh`); `hub_preauthorizations` tem RLS+FORCE (só sessão de sistema), sem DELETE.
- **Smoke com binários reais** (`scripts/e2e-hub-smoke.sh`): estranho recebe 404; e-mail sem conta fica pendente, aparece no painel com o acesso escolhido e só o admin cancela.
- **Vitest** 757/758 (a falha é a do `SettingsShell`, anterior); **Playwright com API MOCKADA** 4/4 (inclui o fluxo novo e o painel visto em captura de tela).
- **NOT VERIFIED:** nenhum login real no Keycloak (se o realm exige e-mail verificado, e como o Keycloak entrega `email_verified`), nenhum e-mail de aviso (não há: o administrador avisa a pessoa), efeito em empresas reais.

## Segunda rodada de correções (revisão do Codex de 7a20153..4bfcf4e) — 2026-10-09
Revisão `task-mv0ejisb-0ff3dw`: **0 CRITICAL, 5 HIGH, 5 MEDIUM, tudo tratado** (ver ADR-0038 "Segunda rodada" e ADR-0039 §3.10). Estado: LAB, commits locais, **não implantado**.
- **HIGH 1** roteamento (job + varredura de liveness) → para empresa suspensa; **HIGH 2** jobs de IA e de mídia (vision/transcrição) → só pegam trabalho de empresa ativa; **HIGH 3** gancho do login → uma transação/uma conexão, 8 s de limite, pool de 2 conexões prova; **HIGH 4** autor demovido durante a aplicação → `lockAuthority` (a aplicação espera e então anula); **HIGH 5** suspensão × concessão → `LockTenantActive` por empresa dentro da mesma transação.
- **MEDIUM:** limpeza de runs de fluxo, reserva de id e reconciliação param para empresa suspensa; aplicação parcial impossível (teste com falha injetada no último passo: nada fica, a autorização continua pendente e a nova tentativa aplica); `DELETE`/`TRUNCATE` revogados em `hub_preauthorizations` (teste: nem sessão de sistema da aplicação apaga).
- **POSTGRES VERIFIED:** pacotes `hub/adapters`, `hub/provisioning`, `worker/{delivery,flows,routing}`, `routing/adapters`, `intelligence/adapters`, `media/adapters`, `platform/db` todos verdes; mutantes dos dois scripts estendidos (resultado abaixo quando concluído).
- **Decisão de risco:** o gancho de login continua síncrono (a pessoa precisa já ter o Hub no primeiro acesso), mas limitado a 8 s e nunca bloqueia o login por erro.

## Terceira e quarta rodadas (re-revisões do Codex de e77b052 e 8945027) — 2026-10-09
`task-mv0gnbxs-m3e5ej`: 0 CRITICAL, 1 HIGH, 4 MEDIUM (atomicidade, `lockAuthority`, `LockTenantActive` e a migration 102 dadas como **fechadas**). `task-mv0holp8-xyusd8`: 0 CRITICAL, 4 HIGH (janelas entre perguntar e agir nas chamadas externas; ramos de resultado; reserva de id; claims) e 1 MEDIUM; resposta: **`WhileActive` segura o lock durante a operação** (ADR-0038 "Terceira rodada").
- **POSTGRES / UNIT VERIFIED:** portão de mídia (arquivo/imagem/áudio, **todos os ramos de resultado dentro do portão**, falha fechada), `WhileActive` (a suspensão espera a operação em curso; depois nada roda), `EnqueueVision`, claims de mídia/análise/IA, varredura de liveness e reconciliação que **pulam** uma suspensão em andamento, limpeza de runs que **espera** por ela, reserva de id sem chamada ao provedor, `TRUNCATE` também negado.
- **Mutantes:** `scripts/test-hub-suspension-mutations.sh`: 36 mortos + 1 equivalente documentado (a falha da pergunta do portão não pode abrir nada: `fn` só roda dentro de `WhileActive`). Suítes de acesso (43), resposta e administração, convite (21): verdes no código desta fase.
- **Aceito e dito:** custo de latência da suspensão (espera a operação externa em curso); resíduo de fila de IA; presença, faxina de anexos e relay do outbox fora do congelamento.
- **Revisão do Codex do commit `70e3649` (WhileActive): `CODEX_PLUGIN_NOT_EXECUTED`** — o Codex respondeu "usage limit" (volta às 05:13 do horário do servidor). **Não** se declara "zero CRITICAL/HIGH" para este commit: os achados da rodada anterior estão tratados por testes e mutantes, mas **sem** uma nova leitura independente. Pendência: rodar `codex-companion.mjs task` sobre `git show 70e3649` quando a cota voltar, antes de implantar.

## Implantação da suspensão total e da autorização por e-mail — 2026-10-09
LAB: imagens `sha-d7bb2a7` (api, worker, web), **migration 102 aplicada**, rollback `rollback-pre-suspension-20261009-*`, backup antes (local, externo e nuvem). Serviços saudáveis, bundle servido = construído, `hub_preauthorizations` com RLS+FORCE e **sem DELETE** para a role da aplicação, rotas novas respondem 401 sem login. Implantado a pedido do dono **com a revisão do Codex de `70e3649` ainda pendente** (`CODEX_PLUGIN_NOT_EXECUTED`: cota até 05:13). Não visto com login real nem webhook real.

## Auditoria de rotas/telas, vocabulário "Instância", Conversas única e origem do canal (ADR-0039 §6) — 2026-10-09

Mudança **só de frontend** (commit local; **não implantada**). Evidência:

- **UNIT (Vitest):** 772 testes, 771 verdes; o único vermelho é `SettingsShell` (já vermelho no HEAD anterior; confirmado com `git stash`). Novos/alterados: `ChannelOrigin`, `CompaniesPanel`, `HubAccessPage` (aba Instâncias única, `ConversationsEntry` com pessoa só-Hub), navegação sem "Hub".
- **E2E com mock (Playwright, navegador real, API mockada):** 11/11 em `hub.mock`, `hub-access.mock`, `hub-companies.mock`. Prova a tela, **não** o backend nem a sessão real.
- **Spike da fase 3:** apenas leitura de código, resultado em ADR-0038 ("Spike da fase 3"). Fase 3 segue **NOT WIRED**.
- **Não verificado:** nenhuma tela foi aberta com sessão real do Keycloak em produção; o aviso do dono de que o layout em produção difere das capturas **não** foi reproduzido (o bundle servido era o construído). O que se viu como diferente é a ausência da fase 3 e a duplicidade já descrita, corrigidas acima.
- **Codex:** `CODEX_PLUGIN_NOT_EXECUTED` para esta mudança.

## Fases 3 e 4 do ADR-0038: gestão delegada, equipes, distribuição e transferência (migrations 103 e 104) — 2026-10-09

Local (commit `8398b7b`), **não implantado**. Evidência:

- **POSTGRES VERIFIED** (banco descartável, papel `omnira_app` sem bypass): `internal/hub/adapters` (gestão: pilha real middleware + serviços de canal + RLS do próprio usuário; escopos separados; grant/contrato/empresa/Hub vivos; capacidade desligada ainda bloqueia; segredo fora da resposta e da auditoria; a RLS recusa sozinha o que a aplicação nunca pediria; outro Hub; lista/flag; transferência com corridas), `internal/hub/distribution` (equipes, rodízio, só quem pode responder, capacidade, fila × instância, dois workers = uma atribuição, **revogação concorrente vence**, conversa travada é pulada), `internal/worker/hubdistributor`.
- **Mutantes (fase 3):** `scripts/test-hub-manage-mutations.sh` — todos mortos por teste real; três camadas redundantes documentadas como **não-mutantes** (o "negar" do lock isolado, o USING da política de UPDATE, o guard do `managed_instances`). Fase 4: ver abaixo.
- **Migrations 093..104:** sobe/desce/sobe idêntico (`scripts/test-hub-migrations.sh`); toda tabela com `tenant_id` tem RLS+FORCE+política; toda função DEFINER fixa `pg_temp` por último.
- **UNIT/Playwright mock:** Vitest 789/790 (`SettingsShell` vermelho desde antes).
- **NOT VERIFIED:** nada foi visto com sessão real do Keycloak; nenhuma conexão de canal foi criada de verdade pelo Hub (os serviços de canal usaram sessão/probe falsos); a distribuição não rodou com conversas reais.

## Revisão do Codex das fases 3 e 4 + E2E em navegador real (ADR-0038 fase 5) — 2026-10-09

**Codex (task-mv0r7tzn-ip44zd, leitura somente) sobre `8945027..8398b7b`:** 0 CRITICAL, **3 HIGH, 1 MEDIUM** — todos corrigidos e provados:
- **HIGH (70e3649)** pânico recuperado *fora* do portão da empresa gravava a falha depois de soltar o lock (poderia ser depois de uma suspensão): agora a recuperação roda **dentro** do portão (`processor`, `vision`, `transcriber`); testes `TestAPanic...` falham no código antigo e passam no novo.
- **HIGH** conta **inativa** mantinha autoridade no Hub: `user_is_active()` (migration 103) entra em `has_hub_manage_access` e (migration **105**) em `has_active_hub_access`; sessões de conta inativa deixam de resolver (`ResolveSession`, provado sob o papel `omnira_app`). *Resíduo declarado:* tokens Bearer (JWT de ferramentas/dev) são verificados só criptograficamente.
- **HIGH** revogação concorrente podia correr contra a chamada ao provedor: `lock_managed_tenant(tenant, user, hub)` agora prende, até o fim da requisição, empresa, Hub, contrato, vínculo, conta e grant (na ordem das rotas administrativas); teste de retenção para cada mudança (`TestHubManagerHoldsEverythingTheAuthorizationDependsOn`). *Custo aceito:* suspender/revogar espera a chamada ao provedor em curso.
- **MEDIUM** distribuição podia passar de `max_open` com dois workers: as atribuições de uma equipe são serializadas na linha da equipe; teste com 8 distribuições concorrentes e capacidade 2. *Limite declarado:* a capacidade só decide quem a distribuição **automática** escolhe; assumir/transferir à mão não conta contra ela.

**E2E em navegador real (`scripts/e2e-hub-browser.sh`, 8 cenários, 8/8 — o 8º é a Auditoria):** API real (`omnira-api` com todas as flags do Hub), `omnira-hubctl`, o bundle web real servido por `vite preview` com proxy `/api`, PostgreSQL descartável com papel sem bypass e **sessões reais** (`auth_sessions`). Cobre: Conversas unificadas com logo de origem e filtro por instância (pessoa só-Hub, sem item "Hub"); assumir e responder (a mensagem cai só na instância certa); leitura somente; transferência (só entre quem pode responder, histórico e auditoria, a API recusa quem não segura); delegação (operador → administrador → agente cria a conexão de CRM pelo Hub; segredo fora de respostas e auditoria; escopo "canais" não delegado dá 403; retirar a chave corta na hora); segurança (agente não vê painel/equipes; instância sem acesso não existe); equipes + distribuição automática (rodízio 2/2, só quem pode responder, histórico como "sistema", idempotente).
**Ainda NÃO provado:** login real do Keycloak, canal WhatsApp real (a conexão de CRM é só **armazenada**), webhooks reais, `Bearer` de usuário inativo.

**Segunda passada do Codex (task-mv0rxjme-bdhrh9, sobre `8398b7b..a7aa6f2`):** 0 CRITICAL, 2 HIGH, 2 MEDIUM; confirmou as quatro correções anteriores e achou:
- **HIGH "000103 editada no lugar":** a premissa não se aplica — a migration 103 **nunca foi aplicada em banco algum** (produção em 102; o banco de revisão nem tem as tabelas do Hub; conferido por catálogo). Editar uma migration ainda não implantada é correto; as já aplicadas (≤102) não foram tocadas. Fica o aviso: a partir do deploy, 103–105 não se editam mais.
- **HIGH "E2E real pode escrever em qualquer banco":** `web/e2e-real/support.ts` agora **recusa** tudo que não seja o banco descartável do script (nome do container, URL do hubctl e banco semeado com exatamente os 5 usuários `@e2e.test`).
- **MEDIUM capacidade entre equipes:** a mesma pessoa em várias equipes burlava o lock por equipe; agora há **um lock por Hub** para a distribuição automática (teste com 20 distribuições concorrentes em duas equipes que compartilham uma pessoa). Limite declarado: assumir/transferir à mão não toma o lock (a capacidade é um limite brando da distribuição **automática**).
- **MEDIUM oráculo de status:** `user_is_active(uuid)` deixou de ser executável pela aplicação (só as funções DEFINER a usam); a sessão usa `session_account_active(session_id)`, que só responde sobre o dono de **uma** sessão (conhecer o id já é o que a torna resolvível).

**Terceira passada do Codex (task-mv0sd1iu-780lx4, sobre `a7aa6f2..400b46c`):** 0 CRITICAL, 1 HIGH, 1 MEDIUM; confirmou o oráculo de status fechado e a corrida de capacidade entre equipes. Corrigidos:
- **HIGH** o guarda do E2E ainda passava por um shell (um valor com `;` escaparia): agora **sem shell** (`execFileSync` com argv fixo; o script só entrega três valores validados por regex: contêiner, URL de aplicação e binário do hubctl) e a verificação do banco semeado vale para cada combinação.
- **MEDIUM** editar equipe (membros, capacidade, instâncias, modo, remover) agora toma o **mesmo lock por Hub** da distribuição (uma edição não é ultrapassada por uma atribuição que já leu os membros antigos) e a atualização do rodízio confere `RowsAffected == 1` (senão a atribuição inteira desfaz); testes de espera e três mutantes novos.

**Auditoria (aba nova) e a passada do Codex sobre ela (task-mv0wfcq4-nzqmcg):** 0 CRITICAL, **1 HIGH**, 1 MEDIUM, 1 LOW — corrigidos:
- **HIGH** uma empresa com contrato com **dois Hubs** deixava o administrador de um ver os eventos atribuídos ao outro: agora só entram (1) eventos atribuídos a **este** Hub (`metadata.hub_id`) e (2) eventos de uma instância deste Hub **sem nenhuma atribuição** (as mudanças da própria empresa); o que é de outro Hub nunca sai, nem para empresa compartilhada (teste com empresa compartilhada e com empresa alheia).
- **MEDIUM** varredura da tabela inteira: consulta em dois ramos com índices parciais novos (migration **106**: `(metadata->>'hub_id', created_at, id)` e `(tenant_id, created_at, id)`).
- **LOW** cursor só por horário pulava eventos com o mesmo instante: cursor `(horário, id)` (teste com cinco eventos no mesmo microssegundo).
Provas: `internal/hub/adapters` (audit), mutantes `scripts/test-hub-audit-mutations.sh` (13 mortos), migrations 093..106 sobe/desce/sobe idêntico.

**Rodada final de mutantes (código final):** `manage` 38, `distribution` 37, `audit` 13, `suspension` (inclui 3 mutantes de "pânico dentro do portão"), `reply` (inclui "conta inativa"), `access`, `admin`, `invite` — **todas PASS**. Mutantes que sobreviveram durante o caminho foram camadas redundantes (documentadas como "não-mutante" nos próprios scripts) ou lacunas de teste (corrigidas com teste novo).

## Implantação das fases 3-5 (2026-10-09, LAB) — sha-faabb5d

- **Antes:** backup `omnira_dev_20261009T121535Z.dump` (local, disco externo e nuvem, conferidos); tags de rollback `rollback-pre-phases34-20261009-0815` (api, worker, web); `.env` antigo em `/data/cafegpt/tmp/env-pre-phases34` (0600).
- **Migrations aplicadas ao banco vivo:** 000103 (gestão delegada), 000104 (equipes), 000105 (conta ativa), 000106 (índices da auditoria) — de 000102 para 000106. Produção tinha 0 equipes, 3 contratos, 3 grants, 0 contas inativas (nada a migrar além do esquema).
- **Imagens** `sha-faabb5d` (api, worker, web) construídas de um worktree limpo do HEAD; api/worker/web recriados e saudáveis; bundle servido = bundle construído.
- **Flag nova:** `OMNIRA_HUB_DISTRIBUTOR_ENABLED=true` (intervalo 15 s) no worker; sem equipe de rodízio criada ela não faz nada. O worker registrou "Hub work-pool distributor started".
- **Conferido no ar:** rotas novas (`managed`, `pools`, `audit`, `instances/.../channels`, `transfer`) respondem 401 sem login; `user_is_active` **não** é executável pela aplicação, `session_account_active` e `has_hub_manage_access` são; `work_pool_instances` com RLS+FORCE; 0 funções DEFINER sem `pg_temp` fixado; 0 panics nos logs de api e worker.
- **NÃO verificado em produção:** nenhuma tela com login real do Keycloak; nenhuma conexão de canal criada pelo Hub no banco vivo; a distribuição não rodou com equipe real (não há equipe); nenhuma sessão viva no momento da conferência (o caminho de sessão foi provado no E2E real e no teste sob o papel `omnira_app`).
- **Rollback:** `docker tag 20-omnira-<svc>:rollback-pre-phases34-20261009-0815 20-omnira-<svc>:latest` + `docker compose up -d --no-deps --force-recreate api worker web`; as migrations 103-106 têm `down` testado (`scripts/test-hub-migrations.sh`); as imagens antigas funcionam com o esquema novo (só acrescentamos), então o rollback de código não exige desfazer migrations.


## 2026-10-09 (tarde) — abas por instância em Conversas + ADR-0040 (proposta)

- **IMPLEMENTED (só front, não implantado):** `ConversationsEntry` passa a mostrar abas (primitivo `Tabs`) quando a pessoa atende 2+ instâncias
  (as próprias + as liberadas pelo Hub): "Todas" (inbox unificada, só se um Hub serve 2+) e uma aba por instância. Instância própria abre a caixa
  completa (navegação completa, como o seletor de instância faz); instância só pelo Hub abre a visão de texto fixada nela, com aviso de visão reduzida.
  Perda de acesso com a aba aberta (listas relidas a cada 15 s, só conclui "perdido" de leitura bem-sucedida): aviso borrado, cache da instância
  descartado, aba some. Sem abas para quem tem uma instância só (comportamento anterior). `?modo=empresa` segue valendo.
- **UNIT:** `ConversationsTabs.test.tsx` (12) + 2 ajustados em `HubAccessPage.test.tsx`; Vitest 805/806 (a mesma falha antiga de `SettingsShell` e o arquivo Playwright coletado).
  Mutantes: 5 mortos (lista sempre confiável, aviso com 1 instância restante, cache mantido, trava de recarga esquecida no carregamento, nunca perdido);
  1 sobrevivente equivalente (clique que não troca a instância: o efeito de troca automática faz o mesmo).
- **HTTP (mock) no navegador, contra o código da árvore:** 17/17 (`npm run test:e2e:local`). **E2E real** (API + hubctl + Postgres descartável + bundle reconstruído): 8/8, cenário 1 ampliado com a barra de abas.
- **Armadilha achada:** `web/playwright.config.ts` aponta `baseURL` para a **produção**; os specs `*.mock.spec.ts` rodados com ele verificam o bundle JÁ implantado,
  não o código local. Use `npm run test:e2e:local` (`playwright.local.config.ts`, build + preview na porta 3187). As rodadas "Playwright mock" anteriores
  a esta data foram contra o bundle implantado.
- **NOT WIRED:** o contexto completo (mídia, contato, ERP) para quem tem acesso SÓ pelo Hub: depende do ADR-0040 (PROPOSTA, nenhuma migration). Até lá essas abas mostram a visão de texto.
- **Sem revisão Codex** desta fatia (front, sem mudança de autorização): `CODEX_PLUGIN_NOT_EXECUTED`.

## 2026-10-09 (noite) — migration 107 e decisões do ADR-0040

- **IMPLEMENTED (local, NÃO implantada):** `000107_channel_credentials_delegated_read_explicit`: a leitura delegada de `channel_credentials` passa a exigir, ela mesma, o escopo do canal da conexão
  (antes dependia do RLS da conexão pai). Sem efeito de comportamento hoje; defesa em profundidade pedida pela revisão externa (ADR-0040 §5).
- **POSTGRES VERIFIED:** `TestHubManagerCredentialReadDoesNotRelyOnTheConnectionPolicy` (abre a política da conexão de propósito e confere que o gestor só lê a credencial do próprio escopo);
  `scripts/test-hub-migrations.sh` 093..107 (sobe, desce e sobe idêntico; catálogo ok); `internal/hub/adapters` ok; gate de integração completo: **33 pacotes ok** (inclui `tenancy/adapters`, verde após as correções do aceite de convite);
  mutação de gestão 39/39 (o mutante novo morre por 1 teste).
- **Decisões do dono:** grupos de WhatsApp ficam só para membros; a granularidade do banco para delegados será avaliada pela análise do ADR-0040 §4.1 (domínio no banco, capacidade fina no serviço).
- **Sem revisão Codex** desta migration: `CODEX_PLUGIN_NOT_EXECUTED`.

## 2026-10-09 (noite, 2) — ADR-0040 Fase 02: núcleo do serviço delegado (migration 108)

- **IMPLEMENTED (local, NÃO implantado; flag `OMNIRA_HUB_SERVE_ENABLED` desligada por padrão):**
  - `000108_hub_delegated_serving_core`: teto do contrato (`delegable_permissions`), permissões da concessão (`permissions`), 4 chaves novas de permissão (`conversation.read/reply`, `media.read`, `contact.read`; nenhum papel as recebe),
    `permission_domains` (chave → domínio + ler/escrever; chave sem linha NUNCA é delegável), `delegated_permissions` (concessão ∩ teto ∩ delegável, ao vivo), `lock_served_tenant` (única porta para o contexto delegado: valida, trava as linhas, define `app.acting_hub`),
    `actor_has_permission` (uma só definição de "pode?", sem união de contextos), `has_delegated_access` (predicado de domínio para a Fase 03), política de leitura das PRÓPRIAS linhas de `audit_events` no contexto delegado.
  - Go: `X-Omnira-Acting-As` (`member` | `hub:<id>`; inválido = 400, nunca vira "member"), `AccessSourceHubServe`, ramo delegado do `AuthorizationMiddleware` (404 uniforme, sem membership), `ActorHasPermission`, auditoria central com `via/acting_as/hub_id/contract_id/grant_id`.
  - **NOT WIRED:** nenhuma política de linha chama os predicados ainda (Fase 03); nenhum módulo usa `actor_has_permission` ainda (cada um migra na sua fase); sem mudança para membros.
- **UNIT:** parser do cabeçalho, contexto delegado, enriquecimento da auditoria (3 pacotes).
- **POSTGRES VERIFIED (papel real `omnira_app`):** 14 testes novos em `serving_integration_test.go`: concessão ∩ teto ∩ delegável; nenhuma chave de equipe/contrato/segredo é delegável (estrutural); vivacidade de CADA elo (concessão revogada/suspensa/expirada/futura, contrato suspenso/revogado/expirado/futuro,
  instância e hub suspensos, conta inativa, teto reduzido, vínculo ao Hub removido) com efeito imediato; só para o chamador, uma instância, um hub; a porta só define o contexto se vivo e ele não vaza para a próxima requisição;
  contexto de membro × delegado sem soma; contexto forjado não confere nada; domínio (escrever implica ler); middleware real (404 uniforme, 400, 403 com a flag desligada, membro inalterado, revogação/suspensão/teto na requisição seguinte);
  o pedido em andamento SEGURA as linhas de que depende; auditoria nomeia hub/contrato/concessão e o agente lê só os próprios eventos.
- **Mutação:** `scripts/test-hub-serve-mutations.sh` **38/38 mortos**. Encontrei e corrigi duas lacunas de teste (oráculo de "fulano pode X" sobre outra pessoa; testes fora do filtro do script).
  Não mutantes documentados no script: guarda do chamador de `lock_served_tenant` (redundante com a de `delegated_permissions`), JOIN do vínculo ao Hub (a FK da concessão já o garante), um dos dois checks de vivacidade da porta, `acting IS NOT NULL` de `has_delegated_access`.
- **Gate:** migrations 093..108 sobem/descem/sobem idênticas; integração completa **33 pacotes ok**.
- **Gate da fase (ADR-0040 §8) — "sem união de privilégios, `tenant_id` nunca autoriza": provado em banco e no middleware.** Item deixado explícito para a Fase 03: no banco, o ramo de membro das políticas (`has_active_membership`) ainda vale no contexto delegado; ao anexar o ramo delegado às políticas, `has_active_membership` precisa passar a negar quando `app.acting_hub` está definido, senão uma pessoa membro+delegada somaria os dois no nível de linha.
- **Sem revisão Codex** desta fase: `CODEX_PLUGIN_NOT_EXECUTED`.
- **ADR-0041 (proposta):** console de administração independente (decisão de produto do dono); só documento.

## Implantação (2026-10-09, LAB) — sha-986ed07: aceite de convite, abas por instância, migrations 107 e 108

- **Antes:** backup `omnira_dev_20261009T201522Z.dump` (local, disco externo e nuvem, checksum conferido); tags de rollback `rollback-pre-serving-20261009-1615` (api, worker, web); `.env` antigo em `/data/cafegpt/tmp/env-pre-serving-20261009-1615` (0600).
- **Migrations aplicadas ao banco vivo:** 000107 (leitura delegada de credencial com escopo explícito) e 000108 (núcleo do serviço delegado), de 000106 para 000108. Produção: 3 contratos e 3 grants, nenhum com teto/permissões delegadas (nada a migrar além do esquema).
- **Imagens** `sha-986ed07` (api, worker, web) construídas de um worktree limpo do HEAD (removido depois); recriadas e saudáveis; id da imagem em uso = id da tag; bundle servido (`index-CmFvwLsE.js`) = bundle construído e contém o aviso de acesso perdido.
- **Flag `OMNIRA_HUB_SERVE_ENABLED`: vazia = DESLIGADA.** Com ela desligada o cabeçalho `X-Omnira-Acting-As: hub:<id>` é recusado (403) e nada do caminho delegado roda. Sem login, rotas de tenant e de Hub respondem 401 (com e sem o cabeçalho).
- **Conferido no ar (catálogo):** 4 chaves novas, **0** role_permissions para elas; `permission_domains` com RLS+FORCE; funções novas executáveis pela role da aplicação e `user_is_active` NÃO; 0 funções DEFINER sem `pg_temp` fixado; política de leitura de credencial com o predicado explícito; política de auditoria do contexto delegado presente; 0 panics e 0 erros nos logs das três imagens.
- **O que muda para as pessoas hoje:** (1) o aceite de convite pela página volta a funcionar (estava em 400 desde 5/out); (2) abas por instância em Conversas para quem atende 2+ instâncias. O serviço delegado em si (mídia/contato para quem só tem Hub) NÃO está ligado.
- **NÃO verificado em produção:** nenhuma tela com login real do Keycloak (aceite de convite, abas); nada do serviço delegado (flag desligada, Fase 03 pendente).
- **Rollback:** `docker tag 20-omnira-<svc>:rollback-pre-serving-20261009-1615 20-omnira-<svc>:latest` + `docker compose up -d --no-deps --force-recreate api worker web`. As imagens antigas funcionam com o esquema novo (só acrescentamos); as migrations 107/108 têm `down` testado (`scripts/test-hub-migrations.sh`).

## 2026-10-09 (noite, 3) — ADR-0040 Fase 03: piloto de atendimento com contexto completo (migration 109)

- **IMPLEMENTED (local até o deploy desta seção):** migration `000109` (predicados de membro falsos enquanto se age por um Hub; `acting_hub()` com leitura segura; leitura delegada de contatos e mídia, herdando a visibilidade e o escopo de filas
  da conversa), rotas delegáveis em **lista de permissão** (`Delegable`, padrão = recusar), mídia por caminho do Hub, assumir/responder pelo caminho de escrita do Hub já revisado (`DelegatedWrites`), `hubctl serving ceiling|grant`,
  `full_context` por empresa, e o modo "atendendo pelo Hub" no front (mesma caixa, cabeçalho só para a instância em que a sessão age, menu reduzido, polling de 15 s, cartão de detalhes somente leitura). Flag `OMNIRA_HUB_SERVE_ENABLED` desligada por padrão.
- **POSTGRES VERIFIED (papel real):** testes de leitura por domínio, escopo de filas do contrato (sem escopo, fila 1, fila 2, lista vazia, lista malformada), nada escrevível pelas políticas delegadas, um Hub nunca lê o que outro Hub pode, contexto forjado/malformado não confere nada e não gera erro,
  membro+delegado recebe só o contexto pedido (o administrador da instância não herda administração), rotas reais (lista, conversa, mensagens, contato, mídia por cabeçalho e por caminho, 403 sem chave, 404 em rota não delegável mesmo para membro+delegado, revogação),
  escrita real (assumir, responder, idempotência, anexo recusado, duas condições exigidas, outro agente 409, outra instância 404 inclusive para quem atende as duas, membro cai no handler original), provisionamento (teto, chaves, coerência assumir/responder/`can_reply`, espera por mudança concorrente do teto).
- **Mutação:** `test-hub-serve-reads-mutations.sh` **28/28**; `test-hub-serve-mutations.sh` (fase 02, retargetada para as definições da 109) **38/38**; front: 16 mutantes do contexto de atuação, todos mortos. Lacunas de teste achadas e corrigidas pelo caminho: item buscado sem filtro de instância, edição de concessão revogada, administrador da instância, hold do contrato.
- **HTTP/UNIT:** Vitest 830 de 831 (a mesma falha antiga `SettingsShell`); navegador com API mockada (`npm run test:e2e:local`) 21 de 26: os 5 que falham (fluxos, atendimento, assuntos) **falham igual no bundle já implantado**, são vermelhos antigos, não desta fase.
- **E2E VERIFIED (navegador real, API e Postgres reais, bundle reconstruído):** 9/9. O cenário novo cobre: aba da instância só por Hub abre a mesma caixa agindo pelo Hub; imagem carregada pelo caminho do Hub (naturalWidth > 0); cartão do contato; menu reduzido; todo pedido da instância com o cabeçalho; assumir e responder gravados;
  sem cabeçalho / outra instância / rotas fora do contexto / cabeçalho malformado recusados; após revogar, 404 imediato e a tela vira o aviso.
- **Gate de integração completo:** 33 pacotes ok; migrations 093..109 sobem/descem/sobem idênticas.
- **Revisão Codex (2026-10-09):** CRITICAL 0, HIGH 1, MEDIUM 4, LOW 1. H-01 improcedente (sem coluna de estado em `hub_memberships`), M-03 falso (a 109 usa `NULLIF`), M-04 e L-01 corrigidos com teste e mutante, M-02 e M-01 aceitos (ADR-0040 §12). Nenhum CRITICAL/HIGH em aberto.
- **NÃO provado:** login real do Keycloak; o contexto completo com uma concessão real de produção (nenhum teto/chave existe lá); desempenho (`EXPLAIN`) das políticas em tabelas grandes; tempo real delegado; classificar/editar contato e chamado no ERP (fase 04).

## Implantação (2026-10-09, LAB) — sha-b9dbfee: ADR-0040 fase 03 (migration 109), flag desligada

- **Antes:** backup `omnira_dev_20261009T214524Z.dump` (local, disco externo e nuvem, checksum conferido); tags de rollback `rollback-pre-phase03-20261009-1745` (api, worker, web); `.env` antigo em `/data/cafegpt/tmp/env-pre-phase03-20261009-1745` (0600).
- **Migration aplicada ao banco vivo:** 000109 (de 000108). Produção: 3 contratos e 3 concessões, **nenhum com teto ou chaves delegadas** (nada a migrar).
- **Imagens** `sha-b9dbfee` (api, worker, web) construídas de um worktree limpo do HEAD (removido depois); recriadas e saudáveis; id da imagem em uso = id da tag; bundle servido (`index-DEu53mtj.js`) = bundle construído e contém o modo "Atendendo pelo Hub".
- **`OMNIRA_HUB_SERVE_ENABLED`: vazia = DESLIGADA.** Com ela desligada o cabeçalho de contexto é recusado (403), nenhuma instância anuncia o contexto completo e o comportamento visível é o da seção anterior (abas, visão de texto do Hub).
- **Conferido no ar:** rotas de instância, de Hub e de mídia por caminho respondem 401 sem login (com e sem cabeçalho); funções novas executáveis pela role da aplicação; as 5 políticas novas presentes; 0 funções DEFINER sem `pg_temp` fixado; 0 panics e 0 erros nos logs.
  **Prova só-leitura no banco real, com a role da aplicação:** um membro real continua sendo membro e enxergando as conversas da sua instância sem contexto de atuação; a mesma pessoa, com um contexto de atuação, deixa de ser membro (0 conversas), como projetado. Nada foi gravado (ROLLBACK).
- **NÃO verificado em produção:** nenhuma tela com login real do Keycloak; o contexto completo com concessão real (precisa de teto + chaves via `hubctl serving ...` e da flag; ver `docs/ops/HUB-PROVISIONING.md`).
- **Rollback:** `docker tag 20-omnira-<svc>:rollback-pre-phase03-20261009-1745 20-omnira-<svc>:latest` + `docker compose up -d --no-deps --force-recreate api worker web`; a 109 tem `down` testado e as imagens antigas funcionam com o esquema novo (as funções de membro só ganharam uma cláusula que vale sempre fora do contexto delegado).

## 2026-10-09 (noite) — contexto completo LIGADO em produção para a conta do dono (a pedido)

- **Feito:** `serving ceiling` e `serving grant --preset atendimento` para `keslley@k3gsolutions.com.br` na empresa **Test Company** (a única instância à qual essa conta tem acesso SÓ pelo Hub; `operator=claude-keslley-test`, auditados:
  `hub.contract.ceiling_changed` e `hub.grant.serving_changed`); `OMNIRA_HUB_SERVE_ENABLED=true` no `.env` e só a api recriada (healthy, 0 panics). `.env` anterior em `/data/cafegpt/tmp/env-pre-phase03-20261009-1745`.
- **Observação:** Test Company LTDA e Keslley_Pessoal são instâncias das quais a conta já é **membro** (tenant_admin): nelas a aba abre a caixa completa pela membership (não pelo contexto delegado), e foi aí que as imagens que não apareciam no Hub passaram a aparecer (abas, implantadas antes). **Test Company** não tem conversas nem mídia (0/0/0).
- **Provado em produção (só leitura, papel da aplicação, ROLLBACK):** a porta abre para Test Company e entrega as 5 chaves; para Test Company LTDA (sem teto) entrega `{}`; `membership.manage` nunca é concedida. Sem login, a rota com o cabeçalho responde 401.
- **NÃO provado:** a tela com login real do Keycloak; mídia pelo contexto delegado com dado real (a instância não tem conversa).
- **Rollback:** `omnira-hubctl ... serving grant ... --keys none` e/ou `serving ceiling ... --keys none` (efeito imediato), ou `OMNIRA_HUB_SERVE_ENABLED=false` + recriar a api.

## 2026-10-09 (noite) — dado de teste em produção: conversa com imagem em "Test Company" (a pedido do dono)

- **Criado** (empresa de teste **Test Company**, id `1d2f1511-6434-4032-b695-a1fb4cd5032a`; nada em outra empresa): contato "TESTE Hub (pode apagar)" (`0aeeed12-4527-4482-978d-bc6b889ce37a`, +5500999990001), conversa `26efc3a1-1c64-40fe-90c1-63aae451e1d6`, duas mensagens recebidas (`8512a701-4a1a-4980-af70-746471e8cba0` texto, `6a49cdd5-220c-4378-915c-4eba67a82385` imagem), linha `message_media` `980f1569-a82c-4b4b-93ef-0174a3e068a9` em estado `clean` e o arquivo PNG 640x360 em `/opt/omnira-media/clean/1d2f1511-6434-4032-b695-a1fb4cd5032a/980f1569-a82c-4b4b-93ef-0174a3e068a9`; `hubctl reconcile` projetou a conversa no inbox do Hub (1 item). Sem canal: dá para ler, ver a imagem e assumir; **responder não** (não há linha de WhatsApp nessa empresa, e criar uma faria o sistema tentar enviar de verdade).
- **Provado em produção (só leitura, papel da aplicação, ROLLBACK):** no contexto delegado a conta vê 1 conversa, 2 mensagens, o contato, a mídia e o item do Hub, e 0 linhas de qualquer outra empresa; a api (usuário do contêiner) lê o arquivo (assinatura PNG conferida).
- **Remover depois:** `DELETE FROM message_media WHERE id='980f1569-a82c-4b4b-93ef-0174a3e068a9'; DELETE FROM hub_inbox_items WHERE conversation_id='26efc3a1-1c64-40fe-90c1-63aae451e1d6'; DELETE FROM messages WHERE conversation_id='26efc3a1-1c64-40fe-90c1-63aae451e1d6'; DELETE FROM conversations WHERE id='26efc3a1-1c64-40fe-90c1-63aae451e1d6'; DELETE FROM contacts WHERE id='0aeeed12-4527-4482-978d-bc6b889ce37a';` (como dono do banco) e `rm /opt/omnira-media/clean/1d2f1511-6434-4032-b695-a1fb4cd5032a/980f1569-a82c-4b4b-93ef-0174a3e068a9`.

## 2026-10-09 (noite) — conferência manual do dono, com login real do Keycloak

- **Relato do dono (manual, produção, conta com login real do Keycloak):** "Apareceu tudo" — aba "Test Company", conversa de teste, imagem, cartão do contato e demais itens do roteiro apareceram.
- **Evidência:** relato humano, sem captura de tela anexada. Fecha a lacuna "NÃO provado: a tela com login real do Keycloak" para o caminho de leitura/exibição no contexto delegado.
- **Continua NÃO provado:** responder pelo contexto delegado com canal real (a empresa não tem linha de WhatsApp), tempo real (SSE) no modo delegado, `EXPLAIN` das políticas novas, classificar/editar contato e chamado no ERP (fase 04).
- Estado segue `LAB`.

## 2026-10-10 — ADR-0040 fase 04a: classificar e editar o contato pelo Hub (migration 110) — LOCAL, NÃO implantada

- **IMPLEMENTED:** migration 110 (políticas delegadas por domínio `contact`: UPDATE de `contacts`, leitura/inclusão/alteração de `contact_account_links`, leitura de `customer_accounts`; duas funções `SECURITY DEFINER` estreitas para os efeitos da reclassificação que tocam conversas; políticas **restritivas** `conversations_acting_hub_only`/`messages_acting_hub_only`); handlers de contato/classificação/contas passam a `actor_has_permission`; `GET me/access` devolve as chaves delegadas; 9 rotas delegáveis novas (lista conferida por `delegable_routes_test.go`); preset `classificacao` do `hubctl`; front (cartão com "Editar contato" e "Tipo de contato" só com `contact.classify`, sem "Interno", só empresas já cadastradas; sem batimento de presença ao atender pelo Hub; `/me/access` relido a cada 15 s). Detalhes e decisões: ADR-0040 §13.
- **POSTGRES + HTTP VERIFIED** (`serving_contacts_integration_test.go`, papel real `omnira_app`): domínio/escrita, só UPDATE (nunca INSERT/DELETE de contato, nunca escrita em conta), escopo de fila, grant revogado, funções com chave/contexto/instância/escopo, rotas reais (tipo, spam que tira da fila, cliente só com conta existente, diretório do ERP e "interno" recusados, contato interno da instância intocável, outra instância 404, chave faltando 403, rota não marcada 404, `/me/access` por contexto, membro+delegado), **duas Hubs na mesma instância** com filas disjuntas, presets provisionáveis.
- **Mutação:** `scripts/test-hub-contacts-mutations.sh` **28/28 mortos** (11 rodados numa versão anterior dos testes, só reforçados depois). Lacunas de teste achadas por sobreviventes e corrigidas: inserção mascarada pelo índice único, conta lida por outra chave, membro+delegado fora do contexto. Não-mutantes documentados no próprio script (camadas redundantes: EXISTS do UPDATE de contato, WITH CHECK de tenant, política de mensagens).
- **Migrations:** `test-hub-migrations.sh` 093..110 sobem, descem e sobem idênticas.
- **Gate de integração completo:** 31 pacotes ok; 2 falhas explicadas: (a) `TestNothingCanBeWrittenThroughTheDelegatedReadPolicies` (fase 03) concedia `contact.classify` e esperava UPDATE de contato = 0 — a 110 muda isso de propósito; o teste passou a conceder só chaves de leitura e passa; (b) `messages/adapters` `TestUploadsBeyondTheConcurrencyBudget...` (concorrência de upload, não toca nesta mudança) falhou sob carga da máquina e passou duas vezes isolado. Depois dos ajustes, `hub/adapters`, `contacts/adapters`, `accounts/adapters`, `tenancy/adapters` e `platform/httpserver` rodaram verdes de novo. (Um teste de `hub/distribution` falhou numa rodada anterior e passou na seguinte: instabilidade sob carga, não investigada a fundo.)
- **Vitest:** 838/839 (a falha é a `SettingsShell` de sempre) + arquivo Playwright coletado.
- **E2E em navegador real:** 9/9 (o spec do contexto completo agora também classifica "Outros", confere gravação e auditoria `acting_as`, e edita o apelido).
- **Codex (2026-10-10):** CRITICAL 0, HIGH 2, MEDIUM 0, LOW 1 — todos tratados (ver ADR-0040 §13). O HIGH-1 (soma entre Hubs) já existia na fase 03 implantada; sem exposição real hoje (um só Hub em produção).
- **NÃO provado:** com login real do Keycloak e dado real em produção; `EXPLAIN` das políticas novas (as restritivas rodam por linha só quando se age por um Hub); recarregar `/me/access` a cada 15 s não foi medido; a classificação "Cliente" pelo Hub depende de haver empresas cadastradas na instância (o diretório do ERP é a 04b).
- Estado segue `LAB`; nada implantado, sem push, sem tag.

## 2026-10-10 (manhã) — fase 04a IMPLANTADA em produção (a pedido do dono)

- **Feito:** backup (`omnira_dev_20261010T130745Z`, cópia externa e na nuvem conferidas), tags de rollback `rollback-pre-phase04a-20261010-0907` (api, web, worker), imagens construídas de worktree limpo em `aafaa95` (`sha-aafaa95`), migration **110** aplicada (ledger em 110), `api worker web` recriados (healthy, bundle servido = `index-CTLTRKrd.js`, 0 erros nos logs). Worktree removido.
- **Concessão (a pedido):** `serving ceiling` e `serving grant --preset classificacao` para `keslley@k3gsolutions.com.br` em **Test Company** (atendimento + `contact.classify` + `account.read`; operador `claude-keslley-test`).
- **Provado em produção (só leitura, papel da aplicação, ROLLBACK):** no contexto delegado a conta tem as 7 chaves, `contact.classify` = verdadeiro, `membership.manage` = falso; a membership não vale (`has_active_membership` = falso com o Hub no contexto).
- **ACHADO (a corrigir na migration 111):** no contexto delegado de Test Company a sessão ainda enxerga `conversations` das outras instâncias servidas pelo MESMO Hub (172 + 20 linhas), pela política legada `conversations_read_hub_delegation` (acesso de Hub da fase ≤02, que a conta já tem pela aba do Hub). Não é acesso novo nem de outra pessoa; o código sempre filtra pela instância do contexto. Mas a barreira do banco deveria prender o contexto a UMA instância (`app.acting_tenant`), e não prende. Plano: `lock_served_tenant` fixa a instância e políticas RESTRITIVAS exigem `tenant_id = acting_tenant()` nas tabelas com política legada de Hub.
- **Rollback:** `docker tag 20-omnira-<svc>:rollback-pre-phase04a-20261010-0907 20-omnira-<svc>:latest` + recriar; a 110 tem `down` testado e as imagens antigas funcionam com o esquema novo (as políticas só valem com `app.acting_hub`).
- **NÃO provado:** a classificação pela tela do dono com login real do Keycloak (roteiro seção I).
- Estado segue `LAB`.

## 2026-10-10 — migration 111 (contexto preso a uma instância) — LOCAL, NÃO implantada

- **IMPLEMENTED + POSTGRES VERIFIED:** ver ADR-0040 §14. `TestTheDelegatedContextIsPinnedToOneInstance`; mutação `scripts/test-hub-pin-mutations.sh` 4/4; migrations 093..111 idênticas; E2E real 9/9; pacotes afetados verdes.
- **NÃO implantada** (sobe junto da próxima entrega). Estado segue `LAB`.

## 2026-10-10 — fase 04b: diretório do ERP e chamado no ERP pelo Hub (migration 112) — LOCAL, NÃO implantada

- **IMPLEMENTED:** ADR-0040 §15 (credencial do ERP lida só pelo servidor via função; conta da empresa validada via função; políticas por domínio `ticket` com escopo de fila e instância; aviso ao cliente pelo caminho de escrita do Hub; rotas `crm/companies`, `conversations/{id}/ticket`, `company-suggestions`; preset `chamados`; painel de chamado no cartão do atendimento, sem Atualizar/Alterar status).
- **POSTGRES + HTTP VERIFIED** contra um **ERP DE MENTIRA** (servidor HTTP do teste): `serving_erp_integration_test.go`. **Nenhum ERP real foi chamado e nenhum chamado real foi criado.**
- **Mutação:** `scripts/test-hub-pin-mutations.sh` 4/4 (111) e `scripts/test-hub-erp-mutations.sh` 22/22 (112); mutantes equivalentes documentados nos próprios scripts (lacunas de teste achadas por sobreviventes e corrigidas: ambiguidade mascarada por conexão sem credencial; contato "fora do escopo" que na verdade era o mesmo contato; reapontamento mascarado pelo índice único).
- **Migrations:** `test-hub-migrations.sh` 093..112 sobem, descem e sobem idênticas. **Vitest:** 841/842 (a falha é a `SettingsShell` de sempre; mais o arquivo Playwright coletado). **E2E em navegador real:** 9/9 sem regressão — o fluxo de ERP NÃO está nesse E2E (precisaria de um ERP de mentira e de uma credencial cifrada no banco do script); a prova do ERP é Postgres + HTTP real + ERP de mentira.
- **Codex (2026-10-10, leu o diff, não executou testes):** CRITICAL 0, HIGH 2 (corrigidos), MEDIUM 3 (1 corrigido, 2 aceitos com justificativa), LOW 1 (coberto) — ver ADR-0040 §15.
- **Gate de integração:** pacotes afetados verdes quando cada pacote usa o seu banco; **num mesmo padrão `./internal/hub/...` os pacotes rodam em paralelo no MESMO banco** e `TestAccess_OnlyAnAdminOfTheHubReachesThePanel` / `TestProvisioning_RunsOnlyInASystemSession...` (contagens globais) falham por interferência — reproduzido também em `c01d0be`, ou seja, anterior à 04b; passam com `./internal/hub/adapters ./internal/hub/provisioning ./internal/hub/distribution` como padrões separados.
- **NÃO provado:** ERP real; login real do Keycloak; `EXPLAIN` das políticas novas; implantação (migrations 111+112 não aplicadas em produção).
- Estado segue `LAB`.
