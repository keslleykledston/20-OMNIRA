# Integration Convergence

Auditoria feita **antes** de qualquer evolução de `internal/integrations`. Pergunta: o que o OMNIRA já resolve, e o que o
"Tenant Integration Gateway" da missão do Hub realmente precisa acrescentar?

Estado: o gateway da missão (`migrations/000096_integration_gateway`, `internal/integrations/*`) **foi retirado da branch
corretiva** e permanece só em `backup/lovable-rebuild-attempt`. Motivos verificados abaixo. Nada deste documento está em produção.

Vocabulário de evidência: IMPLEMENTED · NOT WIRED · UNIT VERIFIED · POSTGRES VERIFIED · HTTP VERIFIED · E2E VERIFIED · BLOCKED.

## Respostas às sete perguntas

### 1. O que já existe no ticketing que resolve idempotência?
- `ticket_external_create_attempts`: `UNIQUE (tenant_id, idempotency_key)`, `request_hash`, `CHECK idempotency_key ~ '^[A-Za-z0-9._:-]{8,128}$'`,
  estados `in_flight | confirmed_success | confirmed_failure | outcome_unknown`, FK composta `(tenant_id, conversation_id)`.
- `internal/tickets/adapters/external_create_attempt_postgres.go`: `Acquire` reivindica a chave de forma atômica (a corrida é decidida
  pela constraint única), devolve a tentativa existente se a chave repetir, e `ErrAttemptIdempotencyMismatch` se a mesma chave chegar com outro payload.
- `ticket_external_status_attempts` faz o mesmo para mudança de status.
- Mensagens de saída: `messages.idempotency_key` + `request_hash`.
- Webhooks de entrada: `channel_webhook_events` com `UNIQUE (connection_id, provider_event_id)`, gravado com `ON CONFLICT ... DO NOTHING`
  (`internal/channels/adapters/postgres.go:132`).

### 2. O que já existe para resolver o runtime do provedor?
- `internal/tool/connectors`: `TicketingConnector` (fronteira neutra a provedor, ADR-0013) e `TicketingConnectorResolver`.
- `internal/tickets/adapters/k3g_runtime_resolver.go`: `K3GTicketingRuntimeResolver` reaproveita a conexão/credencial K3G já configurada
  (`channel='erp'`, `provider='k3g_crm'`) e é a fonte única do conector.
- `internal/channels/application.ProviderRegistry` e `internal/channels/ports.ChannelProvider` para canais (WAHA, Meta).

### 3. O que já existe para credenciais por tenant?
- `channel_credentials` (`tenant_id`, `connection_id`, `ciphertext bytea`): AES-256-GCM, ADR-0009, RLS+FORCE.
- `internal/channels/adapters/crypto`, `meta_secrets.go`; `tenant_ai_integrations.secret_ciphertext`.
- `internal/channels/application/erp_connections.go`: `ERPConnectionService` (criar, testar, listar conexões ERP por tenant).

### 4. O que já existe para IDs externos?
- `account_external_links` (`UNIQUE (tenant_id, provider, connection_id, external_company_id)`).
- `tickets` / `ticket_external_*`: `external_ticket_id`, `provider`. `topic_ticket_links`.
- `messages.provider_message_id`, `reserved_provider_message_id`.

### 5. O que já existe para reconciliação?
- Estado `outcome_unknown` + `internal/tickets/adapters/reconciliation_http.go` (`ListCreateAttempts`, reconcile) e a página
  `web/src/pages/TicketReconciliationPage.tsx`. Permissão `ticket.reconcile`.

### 6. O que pode ser reutilizado?
Tudo acima. Em especial: a tabela de tentativas como **recibo de ação externa**, `channel_webhook_events` como **deduplicação de webhook**,
`channel_connections`/`channel_credentials` como **instância de integração por tenant** e `TicketingConnector` como **contrato do provedor**.

### 7. O que realmente precisa ser abstraído?
Muito pouco, e só quando houver um segundo caso de uso concreto:
- uma **fachada de capacidade por instância** (quem pode "abrir OS", "gerar 2ª via") consultada pelo Hub e por ferramentas de IA;
- a **generalização de "tentativa externa"** para além de `ticket create/status` (ex.: 2ª via de fatura), mas como novo `action_type` sobre o padrão existente.

## Classificação

| Item | Classe | Evidência / decisão |
|---|---|---|
| Idempotência de escrita externa | **EXISTING / REUSE** | `ticket_external_create_attempts` (POSTGRES VERIFIED pelos testes do pacote `tickets/adapters`) |
| Deduplicação de webhook | **EXISTING / REUSE** | `channel_webhook_events` + `ON CONFLICT`. O meu `webhook_deduplication` fazia `SELECT COUNT` e depois `INSERT`, com **corrida**. Pior que o existente. |
| Resolução de runtime do provedor | **EXISTING / REUSE** | `K3GTicketingRuntimeResolver`, `TicketingConnectorResolver` |
| Credencial por tenant | **EXISTING / REUSE** | `channel_credentials`. O gateway guardava `credential_reference` (caminho de cofre) que não existe no projeto. |
| ID externo | **EXISTING / REUSE** | `account_external_links`, `external_ticket_id` |
| Reconciliação | **EXISTING / REUSE** | estado `outcome_unknown` + tela de reconciliação |
| Capacidade por instância | **GENERALIZE** (futuro) | só quando o Hub ou a IA precisar consultar "pode fazer X neste tenant" |
| Ação externa além de ticket | **GENERALIZE** (futuro) | novo `action_type` sobre o padrão de tentativas, não uma segunda tabela |
| `TicketingConnector` e conectores por ERP | **KEEP SPECIALIZED** | ADR-0013: fronteira neutra a provedor, já ativa em produção |
| Adaptadores WAHA/Meta | **KEEP SPECIALIZED** | `internal/channels/*`; o gateway da missão só tinha cópias sem consumidores |
| `integration_instances`, `integration_capabilities`, `external_action_receipts`, `webhook_deduplication` | **DEPRECATE** | duplicam as tabelas acima. Sem RLS/FORCE em 2 delas (`integration_instances`, `external_action_receipts`), o que reprova `TestRLSCompleteness` |
| `internal/integrations/*` | **DEPRECATE** | zero consumidores; referenciava tabelas que não existem na cadeia de migrations |
| `internal/ai/tools` (interface `Tool`) | **DEPRECATE** | duplica `internal/tool` (ports/application/execution/connectors) |
| Autorização de ferramenta de IA por tenant efetivo | **NEW** (futuro) | o `ToolExecutionContext` deve consumir o `TenantContext` Hub do `EffectiveAccessResolver`; implementar em `internal/tool`, não num pacote paralelo |

## Regras para o que vier depois
1. Nenhum componente de UI ou da IA fala com ERP. O caminho é API OMNIRA → `TenantContext` efetivo → `TicketingConnectorResolver`.
2. A UI envia **intenção** (ex.: abrir OS) e parâmetros de negócio; nunca credencial, token de provedor ou `integration_id` arbitrário.
3. Qualquer seletor de instância é validado no servidor contra o tenant efetivo e a capacidade autorizada.
4. Escrita externa sempre passa por tentativa idempotente (padrão `Acquire`) e entra na reconciliação.
5. Uma nova tabela com `tenant_id` nasce com RLS + FORCE + policy (o `TestRLSCompleteness` e `scripts/test-hub-migrations.sh` barram o contrário).
6. Não existe, hoje, integração Hub→ERP. O acesso Hub é **somente leitura** de `tenants`, `conversations`, `messages` e `hub_inbox_items`
   (migrations 094/095). Credenciais e estado de integração ficam invisíveis ao agente do Hub (POSTGRES VERIFIED: `TestHubRLS_DirectAndDelegatedMatrix`).
