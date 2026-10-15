# 05 — Contratos de API

Contrato HTTP: **`contracts/openapi/service-context-v1.yaml`** (36 caminhos). Teste de aderência (`internal/servicectx/adapters/contract_test.go`): as rotas registradas e as documentadas são as mesmas **nas duas direções**; um segundo teste garante que toda operação que altera um registro declara `If-Match`, `412` e `428`, e que toda resposta que devolve um registro declara o `ETag`. `omnira-v1.yaml` **não** foi alterado.

## Convenções (valem para tudo)

| Tema | Regra |
|---|---|
| Tenant | Sempre do `TenantContext` (sessão autenticada). Corpo com campo desconhecido (inclusive `tenant_id`) = `400`. |
| Permissão | Por **chave** (nunca por nome de papel): `service.read/create/update/close/assign/link_external/request_external`. A aplicação pede a chave e o **banco pede de novo** (gatilhos e políticas). |
| Concorrência otimista | `ETag: "<versão>"` em toda leitura/escrita de registro; toda alteração exige `If-Match`. Sem ele `428 precondition_required`; versão velha `412 version_conflict`. A versão é avançada **pelo banco**. |
| Idempotência | `Idempotency-Key` (8–128, `[A-Za-z0-9._:-]`) na criação de sessão, ocorrência e tarefa (opcional) e na solicitação de OS (**obrigatória**). Mesma chave + mesmo pedido = mesma resposta (`200` + `Idempotent-Replay: true`), **mesmo depois de o registro mudar ou fechar**; mesma chave + outro pedido = `422 idempotency_key_reused`. |
| Paginação | Cursor *keyset* opaco (`next_cursor`), nunca `OFFSET`. `limit` inválido = `400`. |
| Erros | `{error, detail?}`; códigos estáveis: `not_found, forbidden, invalid, precondition_required, version_conflict, invalid_transition, reopen_not_allowed, conversation_closed, idempotency_key_reused, not_a_customer_conversation, unknown_result, not_configured, outcome_unknown, request_blocked, external_rejected, external_unavailable, unauthorized, internal`. |
| Flags | `OMNIRA_SERVICE_CONTEXT_ENABLED` (tudo; padrão desligado), `OMNIRA_SERVICE_FINALIZE_HOOK_ENABLED`, `OMNIRA_SERVICE_EXTERNAL_ORDERS_ENABLED` (rotas que pedem algo ao ERP; desligada = `404`). |

## Recursos

| Recurso | Operações |
|---|---|
| `service-sessions` | abrir, listar, ler, classificar (`PATCH`), `status`, `close` (fechar/abandonar), `reopen`, `messages` (por referência), `timeline`, `…/conversations/{id}/service-session` |
| `service-cases` | abrir, listar, ler, `PATCH`, `status`, `assign`, vincular sessão, sessões, `timeline` |
| `service-tasks` | criar, listar (filtros `pending`, `overdue`, responsável, sessão, ocorrência, contato), ler, `PATCH`, `assign`, `start`, `complete`, `cancel`, `reopen` |
| `external-records` | vincular (confere no ERP), listar, ler, `refresh`; `external-record-links/{id}/unlink` |
| `external-requests` (flag) | `POST …/service-sessions/{id}/external-service-orders`, listar, ler, `reconcile`, `resolve` |
| `contacts/{id}/service-context` | contexto agregado do cliente (seleção explícita quando o contato tem vários clientes) |
| `contacts/{id}/service-timeline` | histórico em torno do contato |
| catálogos | `service-categories`, `service-result-codes` (escrita exige `tenant.manage`; nunca delegável) |

## Autenticação headless

As rotas usam a autenticação de tenant existente (sessão OIDC / delegação do Hub). **Aplicação externa por credencial própria** (bearer `omn_ext_…`, escopos) e **entrega de webhooks assinados** são da branch `feat/headless-crm-dual-operation` (ADR-0048) e **não** são reimplementadas aqui — ver `06-events-webhooks.md`. Quando ela for integrada, os escopos novos (`service.*`) entram no CHECK de `scopes` e as mesmas rotas passam a aceitar o bearer; o serviço de aplicação não muda (ADR-09: um só núcleo).
