# 07 — RLS e segurança

## Modelo de acesso das tabelas novas

| Operação | Leitura | Criar | Atualizar | Apagar |
|---|---|---|---|---|
| `service_sessions` | membro ativo da empresa (ou sistema) | `service.create` | `service.update` **ou** `service.close` na política; o gatilho exige a chave **certa** por tipo de mudança | ninguém (`REVOKE DELETE`) |
| `service_session_episodes` | membro ativo | `service.create` ou `service.close` | idem | ninguém |
| `service_counters` | membro ativo | `service.create` | `service.create` | ninguém |
| `service_categories`, `service_resolution_codes` | membro ativo | `tenant.manage` | `tenant.manage` | ninguém (desativa) |

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
| chaves no banco | `TestKeysAreAskedByTheServiceAndByTheDatabase` | — (política/gatilho) |
| imutabilidade/intervalos/apagar | `TestDatabaseGuards` | — |
| versão otimista | `TestOptimisticVersionIsRequiredAndAdvancedByTheDatabase` | — |

Varredura de catálogo (`internal/platform/db/rls_completeness_test.go`): passa com as tabelas novas (RLS + FORCE + política).

## Hub (Fase 5) e IA (Fase 6)

Pendentes; ver `09-workspace-hub.md` e `10-flow-builder-ai.md` quando escritos. Regra já decidida: `service.request_external` **nunca** delegável e **nunca** executável por IA.
