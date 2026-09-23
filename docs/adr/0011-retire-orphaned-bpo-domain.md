# ADR-0011: Retire orphaned `internal/bpo` domain

## Status
Accepted

## Contexto

`internal/bpo` (Account/Ticket/Supervisor/SLA/Audit) foi descoberto durante
o DESIGN.5 (auditoria de realidade do frontend) como um domínio backend
aparentemente substancial — implementado, com testes — mas nunca consumido
fora do próprio pacote. O BPO.0 (gate de arquitetura dedicado) investigou o
pacote em profundidade antes de qualquer decisão de reaproveitamento.

Achados do BPO.0:

- **Totalmente órfão**: nenhum import de `internal/bpo` em qualquer outro
  pacote; nunca registrado em `internal/platform/httpserver/server.go`;
  nunca instanciado em nenhuma composition root.
- **Sem migração/schema vivo**: `adapters/postgres.go` referencia tabelas
  `bpo_accounts`/`bpo_tickets` que não existem em nenhum arquivo de
  `migrations/` — o esquema nunca chegou a existir no banco canônico.
- **Sem teste de integração real**: `adapters/postgres_test.go` é um stub
  de compilação (`TestPostgresAdaptersCompile`), com comentário próprio
  admitindo "Testes reais requerem banco de dados" — nunca executado
  contra Postgres real. Todos os demais testes usam mocks em memória
  (`application/mocks.go`). Nenhum teste de RLS, isolamento adversarial de
  tenant, ou HTTP real existe no pacote.
- **Repositórios ignoram o padrão de tenant-scoping atual**: `postgres.go`
  consulta `r.pool` diretamente (`r.pool.QueryRow(...)`), sem
  `platformdb.QuerierFromContext(ctx, s.pool)`, sem `SET LOCAL
  app.tenant_id`, sem RLS/FORCE RLS — e várias queries (`FindByID`) nem
  sequer filtram por `tenant_id` na cláusula `WHERE`. As interfaces de
  `ports/supervisor_repository.go` (`FindByID`, `FindByUser`) nem aceitam
  um parâmetro de tenant.
- **Rotas HTTP não seguem a convenção atual**: `/api/v1/accounts...` em
  vez de `/api/v1/tenants/{tenant_id}/...` usado por toda rota real
  registrada.
- **`bpo.Ticket` é estruturalmente incompatível** com o domínio real de
  ticket/conversa do Inbox: sem `conversation_id`, sem `contact_id`,
  `AssignedToID`/`CustomerID` são UUIDs soltos sem FK para
  `AgentProfile`/`contacts`. Ativar como está criaria uma segunda verdade
  de ticket.
- **Supervisor não modela a arquitetura de presence atual** (ADR-0010):
  nenhuma referência a Valkey, SSE, `AgentProfile`, `queue_members` ou
  routing — é uma agregação de métricas de SLA por Account, não um
  substituto de monitoramento de presença.
- **Modelo de permissão/auditoria paralelo**: `SupervisorRole` define
  strings de permissão próprias (`view:all_accounts`,
  `manage:ticket_routing`, ...), desconectadas do modelo IAM3
  (`conversation.claim`, `membership.read`, ...) e de
  `PermissionChecker`. `AuditEvent` é um struct próprio, não integrado à
  tabela `audit_events` real já usada pelo produto (ex.:
  `channel.connection_created`).
- **Não modela com segurança o alvo de BPO Hub multi-tenant**: `Account`
  é filho de um único `TenantID` (não um hub agrupando tenants); não há
  conceito de operador atribuído a múltiplos tenants com execução
  tenant-scoped explícita por ação.

## Decisão

**Não reviver nem religar `internal/bpo`. Retirar o pacote.**

O pacote inteiro (`internal/bpo/domain`, `application`, `adapters`,
`ports`) deve ser removido em uma slice dedicada e pequena (`BPO.1 — Safe
Retirement of internal/bpo`), sem afetar router, migrations ou frontend
(nenhum dos três o referencia hoje).

## Por quê

- Isolamento de tenant incompatível com o modelo atual (sem RLS, sem
  `SET LOCAL`, queries sem filtro de `tenant_id`).
- Sem schema/migração — não há nada "vivo" a preservar no banco.
- Risco real de dupla verdade de Ticket se ativado como está.
- Modelos paralelos de IAM (permissões) e auditoria, desconectados dos
  mecanismos reais (IAM3, `audit_events`).
- Convenção de rota HTTP obsoleta (`/api/v1/accounts` sem escopo de
  tenant no path).
- Não implementa o modelo de BPO Hub multi-tenant de forma segura.
- Contagem alta de arquivos/testes não implica prontidão para produção —
  os testes existentes não provam nada sobre Postgres real, RLS, ou
  integração HTTP.

## Regras futuras importantes

1. Um futuro domínio de Tickets standalone deve **estender/reconciliar**
   com a arquitetura canônica de conversa/ticket do Inbox — nunca
   ressuscitar `bpo.Ticket`.
2. Um futuro Supervisor deve usar a arquitetura canônica: `Membership`,
   `AgentProfile`, filas (`queue_members`), presence via Valkey
   (ADR-0010), e o modelo atual de routing/assignment.
3. Comportamento cross-tenant de um futuro BPO Hub deve usar mecanismos
   explícitos e autorizados de hub, mantendo cada ação operacional com
   contexto de tenant explícito e isolamento estrito — nunca bypass
   implícito.
4. `BPO Account` **não deve** ser tratado automaticamente como
   equivalente a `Tenant` nem a `CRM Company` — são conceitos distintos.
5. `SLAConfiguration`/`SLAMetrics` e `AccountType`
   (`operator`/`contact_center`/`reseller`) são **apenas referências
   conceituais** — redesenhar contra a arquitetura atual se e quando
   necessário, não copiar como contrato aprovado.
6. Não copiar o padrão de repositório de pool raw (`r.pool.QueryRow`
   sem `platformdb.QuerierFromContext`/RLS) usado em
   `internal/bpo/adapters/postgres.go` — todo repositório tenant-owned
   futuro deve seguir o padrão real (`SET LOCAL` + RLS + FORCE RLS).
