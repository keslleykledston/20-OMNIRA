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

## Terceira rodada (re-revisão do Codex de e77b052) — 2026-10-09
`task-mv0gnbxs-m3e5ej`: **0 CRITICAL, 1 HIGH (mídia/vision/transcrição), 4 MEDIUM, 3 LOW**; HIGH e MEDIUM tratados (ADR-0038 "Terceira rodada"). Atomicidade, `lockAuthority`, `LockTenantActive` e a migration foram dadas como **fechadas** pelo Codex.
- **POSTGRES / UNIT VERIFIED:** portão de mídia (3 testes unitários: arquivo, imagem, áudio; incluindo falha fechada e resultado tardio não gravado), `EnqueueVision` e `TenantActive` em Postgres, varredura de liveness/reconciliação que **pulam** uma suspensão em andamento, limpeza de runs que **espera** por ela, reserva de id que não chama o provedor, `TRUNCATE` também negado.
- **Mutantes:** 35 em `scripts/test-hub-suspension-mutations.sh` (12 novos), todos mortos; suítes de acesso (43), resposta e administração rodadas de novo no código novo, verdes.
- **LOW aceitos e documentados:** ordem global de locks, `CASCADE` do Hub, job de IA atrasado criado para empresa suspensa.

