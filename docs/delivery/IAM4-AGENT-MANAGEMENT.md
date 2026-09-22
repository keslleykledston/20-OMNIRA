# IAM4 — Agent Management

**Status:** IAM4.1 DONE. Migration `000038`; full gates locais PASS.

## Evidência atual (2026-09-22)

- Fresh Postgres: runner oficial aplicou `000001` → `000038`; ciclo `000038` up → down → up passou.
- Backfill Postgres real: somente Membership com `queue_members` recebeu AgentProfile `active`; Membership sem fila não recebeu perfil.
- Teste real de API/RLS cobre admin/supervisor, agent negado, UUID estrangeiro, add/update/remove de assignment, `FORCE RLS` e runtime `NOSUPERUSER/NOBYPASSRLS`.
- Routing real direcionado passou após preservar claim manual para conversa sem fila; conversa com fila continua exigir profile ativo, Membership ativa, disponibilidade e capacity local.
- UI agora abre detalhe de AgentProfile e usa API real para listar/adicionar/remover filas e alterar elegibilidade/capacity. Sem CRUD Queue, presence, skills ou métricas.
- Fixture `internal/e2e` representa Membership ativa, AgentProfile ativo e queue membership válida. Teste de routing real prova inelegibilidade imediata para perfil desabilitado e Membership revogada.
- `go test ./...`, `go vet ./...`, builds Docker API/worker, TypeScript, Vitest 106/106, frontend production build, IAM4 Playwright 2/2 e full Playwright 23/23 passaram.
- DNS Docker voltou a resolver; build e browser stack reais completaram. O E2E IAM4 restaura o estado operacional que altera, preservando a regressão do Inbox.
- Dívida fora da slice: presence e heartbeat/last_seen → IAM4.2; skills → IAM4.3; migração do legado `/users/agents` futura.

## Decisões canônicas

- Membership continua sendo User↔Tenant, papel, permissões e estado de acesso. Não recebe estado operacional.
- AgentProfile é extensão operacional opcional 1:1 de Membership, no mesmo Tenant. Papel e perfil são independentes; nenhum perfil é criado por nome de role.
- Supervisor pode ter AgentProfile; `tenant_agent` não é autoridade de routing.
- IAM4.1 cobre foundation do perfil, agentes operacionais, associações a filas, elegibilidade/availability por fila, capacity por fila, RLS, permissions, audit e testes.
- Não cobre presence, heartbeat, `last_seen`, skills, global capacity, métricas ou dashboard realtime.
- `queue_members.available` significa elegível para receber trabalho **naquela fila**; não significa online, offline, away ou busy.
- Capacity continua em `queue_members`; limite global fica para IAM4.2 se houver requisito explícito.
- Perfil é criado/ativado por fluxo administrativo operacional, não automaticamente por role; desabilitar é preferível a apagar.
- `agent.read` e `agent.manage` só existem porque IAM4.1 terá endpoints. Admin e supervisor recebem ambas; agent não recebe acesso geral; system/hub mantêm o modelo atual sem acesso tenant implícito.
- Configurar filas de um agente é `agent.manage`; CRUD de definição de Queue fica fora de IAM4.1 (`queue.manage` futuro).
- Mutations auditam `agent.enabled`, `agent.disabled`, `agent.queue_assigned`, `agent.queue_removed`, `agent.queue_availability_changed` e `agent.queue_capacity_changed`, sem PII em metadata.
- UI separa "Equipe e acesso" (Membership) de "Agentes" (operação); não mostra presence, last seen, skills ou performance inexistentes.

## IAM4.1 implementation plan

### Schema delta

- Migration `000038`: `agent_profiles(id, tenant_id, membership_id, status active|disabled, created_at, updated_at)`.
- Adicionar `UNIQUE (tenant_id, id)` a `memberships` e `UNIQUE (tenant_id, membership_id)` a `agent_profiles`; FK composta `(tenant_id, membership_id)` impede perfil para Membership de outro Tenant.
- `agent_profiles` recebe ENABLE RLS, FORCE RLS e grants mínimos da runtime role, seguindo `000015`.
- Não renomear nem mover `queue_members.available`/`capacity`; API valida que o profile ativo, sua Membership e a Queue pertencem ao Tenant antes de criar/alterar o membro.
- O backfill `000038` cria profiles `active` somente para memberships comprovadas por `queue_members`; aborta e nomeia dado inválido sem Membership do mesmo Tenant. Nunca infere por `tenant_agent`.

### Permission delta

- Inserir `agent.read` e `agent.manage` em `permissions`.
- Conceder ambas somente a `tenant_admin` e `tenant_supervisor`; sem grants a `tenant_agent`, `hub_admin` ou `system_admin` por `role_permissions`.
- Endpoints resolvem permission da Membership ativa, nunca role string.

### API delta

- `GET /tenants/{tenant_id}/agents` e `GET /tenants/{tenant_id}/agents/{agent_profile_id}` exigem `agent.read`.
- `POST /tenants/{tenant_id}/agents` cria perfil para `membership_id`; `PATCH .../agents/{agent_profile_id}` ativa/desativa; exigem `agent.manage`.
- Detail inclui as queue assignments; `POST .../agents/{agent_profile_id}/queues`, `PATCH .../queues/{queue_member_id}` e `DELETE .../queues/{queue_member_id}` exigem `agent.manage`.
- POST associa Queue; PATCH altera apenas availability/capacity; DELETE remove a associação. Não expor CRUD genérico de Queue.

### Service and routing delta

- Handler operacional concentra validação tenant-aware, lifecycle soft-disable, queue membership e auditoria.
- Routing preserva o algoritmo e passa a exigir AgentProfile ativo para elegibilidade: round-robin junta o profile à Membership/queue member; assign, claim e transfer usam a mesma verificação operacional além de `conversation.claim`/`conversation.manage`.
- O legado `GET /users/agents` mantém sua semântica e `conversation.manage`; a migração dele é dívida futura explícita.

### UI delta

- Adicionar rota Settings coerente com `TeamPage`, por exemplo `/settings/agents`.
- Lista: nome, role, filas, estado operacional e capacity por fila; dados reais somente.
- A lista, o controle de ativar/desativar e a administração de associações usam permissões efetivas `agent.read/manage` e APIs reais; há estados de erro e falta de permissão.

### Tests and migration

- Postgres real cobriu admin/supervisor manage, agent negado, UUID cross-tenant, RLS/FORCE, runtime sem bypass, perfil desabilitado, Membership revogada e queue membership operacional.
- Regressão: round-robin, claim, assign, unassign e transfer verdes; contrato OpenAPI e frontend cobrem endpoints reais.
- Gate de migration: banco vazio → todas migrations → up/down/up da 000038 → RLS completeness/policy coverage → testes da wave: PASS.

### Risks

- Profile opcional exige decisão de rollout para queue_members já existentes; nunca criar por role de forma silenciosa.
- Exigir profile ativo no routing altera elegibilidade atual; fixtures e dados de operação devem ser migrados explicitamente.
- A atualização do endpoint legado de seleção de agentes deve preservar as regras de `conversation.manage` do fluxo de co-atendimento.
