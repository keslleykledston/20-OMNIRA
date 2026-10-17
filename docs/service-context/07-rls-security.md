# 07 — RLS e segurança

## Modelo de acesso das tabelas novas

| Operação | Leitura | Criar | Atualizar | Apagar |
|---|---|---|---|---|
| `service_sessions` | membro cuja função tem **`service.read`** (ou sistema); `service_read_tenants()` calcula as empresas uma vez por comando | `service.create` | `service.update` **ou** `service.close` na política; o gatilho exige a chave **certa** por tipo de mudança | ninguém (`REVOKE DELETE`) |
| `service_session_episodes`, `context_events`, `service_cases`, `service_case_sessions`, `service_tasks`, `service_command_receipts` | membro com `service.read` |
| `service_session_episodes` (escrita) | — `service.create` ou `service.close` | idem | ninguém |
| `service_counters` | `service.read` **ou** `tenant.manage` (000155: quem configura o prefixo precisa ver a linha) | só `tenant.manage`, e só reserva (`last_value = 0`); a linha nasce no 1º número pela função `service_issue_protocol` (000156) | só `tenant.manage` (trocar prefixo reservado); o valor do contador só a função move (gatilho `service_counters_guard`), de 1 em 1; prefixo congela após o 1º protocolo | ninguém |
| `service_categories`, `service_resolution_codes` | membro com `service.read` | `tenant.manage` | `tenant.manage` | ninguém (desativa) |
| `service_cases` | membro com `service.read` | `service.create` (+`service.assign` se já atribuído) | `service.update` / `close` / `assign` **por tipo de mudança** (gatilho) | ninguém |
| `service_tasks` | idem | `service.create` (+`service.assign` se para outra pessoa/time) | idem | ninguém |
| `service_case_sessions`, `service_command_receipts` | idem | `service.update` ou `service.create` | ninguém (histórico) | ninguém |
| `context_events` | idem | qualquer `service.*`, **só** `actor_type='user'` com o próprio id | ninguém (imutável) | ninguém |

A **aplicação** pede a chave antes (`Authorizer`), e o **banco** pede de novo: o mesmo SQL cru, executado como a mesma pessoa, é recusado (`42501`). Isso é mais estrito que o padrão das tabelas operacionais (onde o RLS só isola empresas) e cobre o requisito de distinguir read/create/update/close/assign/link_external/request_external **no banco**.

## Contexto de tenant

- Sessão de membro: `WithTenantSession` (RLS por `has_active_membership`), todo SQL também filtra `tenant_id = $1` com o tenant do `TenantContext` — nunca do corpo/URL como autoridade.
- Sessão de sistema (worker, fluxos, hook do Finalize): `WithSystemTenantSession` **contorna o RLS**; por isso o predicado `tenant_id` do SQL é a única barreira ali. Teste dedicado (`TestASystemSessionOfAnotherCompanyStillCannotReachASession`) + mutação provada (removendo o predicado, o teste falha).

## Testes e mutações (Fase 1)

| Defesa | Teste | Mutação que o teste detecta |
|---|---|---|
| trava da conversa (uma sessão ativa) | `TestParallelOpensOnOneConversationCreateOneSession` | remover `FOR UPDATE` |
| contador transacional | `TestProtocolIsUniqueAndDenseUnderConcurrencyAndPerCompany` | trocar por `MAX+1` |
| savepoint do hook (finalizar nunca bloqueia) | `TestAFailingHookNeverBlocksFinalizing` | remover o savepoint |
| predicado de tenant no SQL | `TestASystemSessionOfAnotherCompanyStillCannotReachASession` | remover `tenant_id = $1` |
| chaves no banco | `TestKeysAreAskedByTheServiceAndByTheDatabase`, `TestClosingAndCompletingNeedServiceClose`, `TestAssignmentNeedsItsOwnKey` | — (política/gatilho) |
| máquina de estados contra SQL cru | `TestTheDatabaseHoldsTheSessionStateMachineAgainstRawSQL` | permitir editar sessão encerrada (provado) |
| episódios | `TestANewEpisodeCannotReachBackIntoTimeAlreadyCounted`, `TestEpisodesCannotOverlapNorSkipNorBelongToAnEndedSession` | remover a checagem do episódio anterior (provado) |
| ator não forjável | `TestAnEventCannotBeSignedAsSomeoneElseNorWrittenWithoutAKey` | aceitar qualquer `actor_type` (provado) |
| leitura exige a chave | `TestReadingNeedsServiceReadInTheDatabaseToo` | remover `service.read` de `service_read_tenants()` (provado) |
| recibo de idempotência | `TestAKeyRemembersTheAnswerEvenWhenItWasAnExistingSession` | não deixar recibo p/ sessão existente (provado) |
| tenant do evento / dedup | `TestAnEventBelongsToTheCompanyOfTheSessionThatWritesIt`, `TestADedupKeyReusedForAnotherFactIsRefused` | confiar no tenant do chamador / ignorar payload diferente (provados) |
| auditoria de ações de sistema | `TestSystemActionsAreAuditedAndNameWhoTheyWereDoneFor` | exigir ator (provado) |
| imutabilidade/apagar | `TestDatabaseGuards`, `TestHistoryIsImmutableForEveryone`, `TestDatabaseGuardsOfCasesAndTasks` | — |
| versão otimista | `TestOptimisticVersionIsRequiredAndAdvancedByTheDatabase`, `TestOptimisticConcurrencyOnTasksAndCases` | — |
| contrato HTTP | `TestOpenAPIAndRoutesAgree`, `TestOpenAPIDeclaresTheConcurrencyContract` | remover um ETag do spec (provado) |

Varredura de catálogo (`internal/platform/db/rls_completeness_test.go`): passa com as tabelas novas (RLS + FORCE + política).

## Hub (Fase 5) e IA (Fase 6)

Feitos: ver `09-workspace-hub.md` (delegação, escopo de fila, painel) e `10-flow-builder-ai.md` (nós, ferramentas, métricas). Regra: `service.request_external` **nunca** é delegável e **nunca** é executável por IA (teste de domínio fixa o conjunto de ferramentas; `permission_domains` não a contém).


## Riscos residuais conhecidos (decisão do dono quando for o caso)

| # | Risco | Estado |
|---|---|---|
| R-1 | Uma sessão de **sistema** (worker, fluxo, hook) contorna o RLS: o que impede o dano é o código confiável (predicado `tenant_id` em todo SQL, gatilhos que valem também para o sistema, a fachada de fluxo exigindo run↔conversa). SQL cru dentro de uma sessão de sistema pode forjar o que quiser | modelo de código confiável; testado por mutação onde há defesa; sem defesa adicional possível sem remover o bypass do sistema |
| R-AI-1 | **FECHADO (2026-10-17, decisão do dono: "Pode")**. A origem da chamada em `POST …/ai/tools/invoke` passou a ser decidida pelo servidor a partir de como o chamador se autenticou, não pelo corpo: `proposed_by=agent` só vale em sessão interativa (cookie do navegador ou token de dispositivo do app) e executa como a pessoa; um token Bearer (script, cliente de IA) é sempre origem `ai` (escrita fica `pending_approval`) e pedir `agent` com ele devolve 403 e não grava nada. Limite honesto: um cliente de IA que dirija o navegador logado com o cookie da pessoa continua sendo, para o servidor, a pessoa | teste `TestAgentOriginNeedsAnInteractiveSessionNotAClaimInTheBody` (mutação de `interactiveSession` pega) |
| R-2 | `completed_by_user_id` e demais "atores" de uma sessão de sistema são validados por membership ativa (000152) mas o sistema pode agir "em nome de" quem o código confiável indicar | idem R-1 |
| R-5 | ~~`service.create` + SQL cru escolhe o prefixo e o primeiro número do contador~~ | **FECHADO (000156).** O contador só é escrito pela função `service_issue_protocol` (`SECURITY DEFINER`): ela pede `service.create` (ou sistema; em delegação, só a empresa atuante), fixa o início (1001 sessões / 1000 ocorrências) e o prefixo `OC` das ocorrências, e deriva o prefixo da sessão do nome da empresa (primeiro candidato livre). O papel da aplicação só **reserva** prefixo (`last_value = 0`, com `tenant.manage`); o gatilho recusa qualquer outra linha nova ou mudança de valor vinda dele. Testes + mutações: `TestOnlyTheIssuingFunctionWritesACounter`, `TestTheIssuingFunctionAsksTheKeyAndTheCompany`. Limite honesto: a função e o gatilho assumem que o dono das migrations é superusuário/BYPASSRLS (já é premissa de outras funções `SECURITY DEFINER` do projeto) |
| R-6 | ~~papel com `service.create` sem `service.read` falha no meio da abertura~~ | **FECHADO.** Toda escrita do módulo pede também `service.read` (recusa 403 limpa antes de escrever nada; `TestAWriteKeyWithoutServiceReadIsRefusedUpFront`). A configuração do catálogo (`tenant.manage`) fica de fora |
| R-7 | ~~aviso ao cliente na rota delegada da instância enfileirado numa transação própria: um commit que falhasse depois do envio avisaria de um protocolo não gravado~~ | **FECHADO.** O aviso é registrado com `platformdb.AfterCommit` e só roda depois que a transação do pedido confirma; trabalho de um savepoint desfeito é descartado com ele. Testes + mutação: `TestAfterCommitRunsOnlyWhenTheSessionCommits`, `TestTheCustomerIsNotToldAboutAProtocolTheRequestRolledBack` (um pedido que morre depois do handler não avisa ninguém) |
| R-8 | Mudança de status do chamado ERP pelo Hub (migration 157/158): as tentativas (`ticket_external_status_attempts`) são legíveis/atualizáveis por qualquer atendente do Hub com a chave do domínio `ticket` e a conversa no escopo do contrato, não só pelo autor; a linha não grava o Hub (só o `actor_user_id`) | **aceito**: é o mesmo formato das tentativas de criação (000112) e dos membros, e é necessário (outro atendente que assume a conversa repete a mesma intenção e concilia; o hash exclui o ator de propósito). O estado da tentativa só muda pelo repositório. A leitura de `tenant_entitlements` por delegado foi estreitada na 158 a UMA chave (`hub_ticket_status`) da instância atuante. Codex 2026-10-17 (MEDIUM/LOW) |
| R-9 | O aviso do protocolo na rota delegada é enviado depois do commit e, se o envio falhar, **só se registra em log** (sem retry): o cliente pode não receber o aviso, nunca recebe aviso de protocolo que não existe | **aceito** (cortesia; a chave `service-opened/closed-<sessão>` impede duplicar). Retry durável exigiria outbox próprio |
| R-10 | Gate 4 (migration 164): uma aplicação externa com `events.read` mas SEM `service.read` não recebe o conteúdo dos fatos de serviço, mas o contador do diário (`next_cursor`/`head`) anda quando eles acontecem — pode inferir **que** algo aconteceu na empresa, não o quê | **aceito**: é o mesmo sinal que já existe para fatos de conversas fora da fila da instalação (a sequência é por empresa e densa, requisito do reencontro sem lacunas). Codex 2026-10-17 (MEDIUM) |
| R-11 | O worker confere escopo/visibilidade e só então faz a requisição HTTP: uma entrega **já em voo** pode sair se o escopo for retirado nesse intervalo | **aceito**: inerente a qualquer entrega assíncrona e igual ao que ocorre com a visibilidade de conversa; as entregas ainda na fila são revalidadas e canceladas (`not_visible`) |
| R-3 | Testes com `sleep` fixo (episódios, timeline, HTTP) podem ficar frágeis sob carga | aceito; sem falha observada nas execuções registradas |
| R-4 | Teste de finalização concorrente usa `time.After(700 ms)` para dar tempo à goroutine; testes de lease envelhecem registros artificialmente em vez de simular uma chamada ERP viva | aceito |
