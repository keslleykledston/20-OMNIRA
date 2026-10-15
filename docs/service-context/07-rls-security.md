# 07 — RLS e segurança

## Modelo de acesso das tabelas novas

| Operação | Leitura | Criar | Atualizar | Apagar |
|---|---|---|---|---|
| `service_sessions` | membro cuja função tem **`service.read`** (ou sistema); `service_read_tenants()` calcula as empresas uma vez por comando | `service.create` | `service.update` **ou** `service.close` na política; o gatilho exige a chave **certa** por tipo de mudança | ninguém (`REVOKE DELETE`) |
| `service_session_episodes`, `context_events`, `service_cases`, `service_case_sessions`, `service_tasks`, `service_command_receipts`, `service_counters` | membro com `service.read` |
| `service_session_episodes` (escrita) | — `service.create` ou `service.close` | idem | ninguém |
| `service_counters` | (acima) | `service.create` | `service.create` | ninguém |
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

Pendentes; ver `09-workspace-hub.md` e `10-flow-builder-ai.md` quando escritos. Regra já decidida: `service.request_external` **nunca** delegável e **nunca** executável por IA.
