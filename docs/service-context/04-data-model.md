# 04 — Modelo de dados

Migrations na faixa reservada **000150–000159** (aplicadas por `tools/migrate-sql.sh`, uma transação por arquivo). Tudo aditivo. Cada tabela: `tenant_id` + FKs compostas `(tenant_id, id)`, RLS `ENABLE`+`FORCE`, gatilho `forbid_tenant_change()`, `REVOKE DELETE` do `omnira_app` (nada é apagado pela aplicação; catálogos desativam).

## 000150 — sessões, protocolo, catálogos (Fase 1)

| Tabela | Papel | Chaves e regras |
|---|---|---|
| `service_counters` | contador por empresa e tipo (`session`, `case`) | PK `(tenant_id, kind)`; `prefix ^[A-Z]{2,6}$`; `last_value ≥ 0`. Alocação: `INSERT … ON CONFLICT DO UPDATE SET last_value = last_value + 1 RETURNING` na mesma transação que insere a sessão (trava de linha por empresa; rollback devolve o número). |
| `service_categories` | categorias da empresa | `UNIQUE(tenant_id, code)`, `code ^[a-z0-9][a-z0-9_.-]{0,39}$`, `active`. Não se apaga. |
| `service_resolution_codes` | resultados próprios que **apontam** a um canônico | `canonical_code ∈ 8 canônicos`; `code` não pode ter nome canônico. Os 8 canônicos são embutidos (não são linhas). |
| `service_sessions` | o atendimento | ver abaixo |
| `service_session_episodes` | trechos em que a sessão esteve em curso | `UNIQUE(tenant_id, session_id, seq)`; um único aberto por sessão (`ended_at IS NULL`); `[started_at, ended_at)` meio-aberto |

### `service_sessions`

`id`, `tenant_id`, **`protocol`** (`^[A-Z]{2,6}-[0-9]+$`, único por empresa), **`protocol_sequence`** (único por empresa), `external_protocol` (número legado/importado, nunca ocupa a faixa gerada; único por empresa quando presente), `conversation_id` (nullable: atendimento sem conversa), `contact_id`, `customer_id` (→ `customer_accounts`), `queue_id` (o setor: filas são setores, ADR-0043), `category_id`, **`status`** (`open|in_progress|waiting|closed|abandoned`), `resolution_code`, `resolution_canonical` (8 canônicos), `resolution_note` (≤1000, sem credenciais), `origin_channel` (`^[a-z0-9_]{1,40}$`), `opened_source`/`closed_source` (`operator|finalize|flow|api|system`), `opened_by_user_id`/`closed_by_user_id`, `opened_at`, `first_response_at`, `closed_at`, `reopen_count`, `idempotency_key`+`request_hash` (parcial único por empresa), `version`, `created_at`, `updated_at`.

CHECKs: `status ∈ {closed,abandoned} ⇔ closed_at IS NOT NULL ⇔ resolution_code IS NOT NULL`; `resolution_code IS NULL ⇔ resolution_canonical IS NULL`; `closed_at ≥ opened_at`; `idempotency_key IS NULL ⇔ request_hash IS NULL`.

Índices: **`service_sessions_one_active_uq`** `(tenant_id, conversation_id) WHERE status IN (open,in_progress,waiting)` (no máximo uma ativa); `(tenant_id, conversation_id, opened_at DESC)`; `(tenant_id, contact_id, opened_at DESC)`; `(tenant_id, customer_id, …)`; `(tenant_id, status, opened_at DESC, id DESC)`; `(tenant_id, queue_id, …)`.

Gatilhos de linha (`SECURITY INVOKER`):
- `BEFORE INSERT`: a nova sessão da conversa só abre depois do `closed_at` da anterior (`23P01`).
- `BEFORE UPDATE`: identidade imutável (`id`, `tenant_id`, `protocol*`, `conversation_id`, `opened_at`, `opened_source`, `opened_by_user_id`, `created_at`); **`version := version + 1` e `updated_at := now()` feitos pelo banco**; chave exigida por **tipo de mudança** (`service.close` ao entrar/sair de closed/abandoned; `service.update` no resto; a sessão de sistema é exceção); reabertura só da sessão **mais recente** da conversa.

### Decisões de modelagem

- **Sem coluna em `messages`**; a ligação é por episódio (ver `01-architecture.md` §3).
- **Responsável atual** não é copiado: lê-se `conversations.assigned_to_user_id`.
- **Contrato do cliente** não é entidade do OMNIRA: será `external_records(type='contract')` (Fase 4).
- **Dados históricos**: nenhuma sessão é inventada para as conversas já encerradas.

## Próximas migrations

`000151` `context_events` · `000152` `service_cases`, `service_case_sessions`, `service_tasks` · `000153` `external_records`, `service_external_links`, `external_record_requests` · `000154` delegação do Hub. Este documento é estendido a cada fase.
