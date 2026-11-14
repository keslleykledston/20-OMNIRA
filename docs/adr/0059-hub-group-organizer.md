# ADR-0059: Organizador contextual de grupos WhatsApp no Hub (metadados, multi-tenant)

**Status:** Accepted (owner aprovou em 2026-11-11, decisão registrada nesta sessão)
**Relacionado:** ADR-0015 (grupos WhatsApp read-only, tenant-owned), ADR-0038 (gestão delegada pelo
Hub), ADR-0040 (contexto completo de instância pelo Hub), ADR-0042 (contexto de sessão inforjável),
ADR-0051 (superfície externa de leitura de grupos — não aplicável aqui, ver seção "Alternativas").

## Contexto

Um pacote de especificação externo ("Smart Group Organizer 2.1") pediu uma camada no Hub BPO para
organizar grupos WhatsApp visualmente (pastas por Tenant, Kanban, drag-and-drop), permitindo que um
mesmo grupo WhatsApp apareça vinculado a mais de um Tenant (ex.: Tenant Alpha e Tenant Beta, onde
Alpha compra um serviço de Beta) e relações comerciais entre Tenants.

Hoje, `wa_groups`/`wa_group_messages` são tenant-owned com FORCE RLS, e a matriz de delegação do Hub
(`docs/architecture/HUB-DELEGATION-TABLE-MATRIX.md:71-73`) marca essas três tabelas como "só membro"
por decisão consciente do dono em 2026-10-09 (ADR-0040 §"Escopo", linha 15). Essa decisão **não é
revertida por esta ADR** — ela continua valendo integralmente para `wa_groups`/`wa_group_messages`.

O que esta ADR decide é uma camada **separada**, no Hub, que guarda só metadados mínimos de catálogo
e vínculos contextuais, nunca mensagens, nunca acesso a `wa_group_messages`.

## Decisão

### 1. Reusar o mecanismo de delegação de gestão já existente (ADR-0038/migration 000103), não criar
um novo sistema de autorização

A investigação do checkout mostrou que o Hub já tem exatamente o mecanismo que esta proposta
precisa: `hub_tenant_service_contracts.management_scopes TEXT[]` (hoje `'channels'`/`'integrations'`,
com `'team'`/`'queues'`/`'settings'` reservados) + `effective_access_grants.can_manage` +
a função `has_hub_manage_access(tenant_id, user_id, scope, hub_id)` usada nas policies de RLS
(migration `000103_hub_management_delegation.up.sql`).

Em vez de inventar uma nova família de permissões "group_metadata.organize" (como o pacote propunha
provisoriamente), a decisão é: **adicionar `'groups'` à lista de scopes permitidos em
`management_scopes`**. Isso significa:
- Um contrato Hub↔Tenant só delega "organizar grupos" se `'groups'` estiver em `management_scopes`
  daquele contrato especificamente — nada é concedido por padrão.
- Um agente do Hub só pode agir se, além disso, tiver `can_manage=true` no seu grant para aquele
  Tenant (ou for `hub_admin`, mesma regra que já vale para `channels`/`integrations`).
- Nenhuma mudança em `permissions`/`role_permissions` do Tenant (`group.read`/`group.manage`
  continuam exatamente como estão, intocados).

Isto satisfaz a regra do projeto de reutilizar mecanismos existentes em vez de duplicar, e reduz a
superfície nova de auditoria/teste, porque a maquinaria de validação (status do Hub, validade do
contrato, validade do grant, revogação, `lock_managed_tenant`, `managed_instances`) já existe e já
tem testes adversariais (`TestHubRLS_*`).

### 2. Catálogo e vínculos ficam em tabelas novas, nunca em `wa_groups`

Migrations aditivas (próximo número livre confirmado: **000185**):

**`hub_group_catalog`** — um registro de metadados por origem verificável:
- `id UUID PK`, `hub_id`, `source_tenant_id`, `source_channel_connection_id`, `provider_group_id`
  (mesma regex de `wa_groups`), `display_name`, `source_state`
  (`available|stale|unavailable|archived`), `version BIGINT`, timestamps.
- `UNIQUE (hub_id, source_tenant_id, source_channel_connection_id, provider_group_id)`.
- FK composta `(source_tenant_id, source_channel_connection_id) → channel_connections(tenant_id, id)`
  — mesma defesa contra forjar conexão de outro tenant que `wa_groups` já usa.
- `UNIQUE (id, hub_id)` — alvo de FK composta das tabelas filhas (mesmo padrão de `work_pools`).
- **Sem** FK direta para `wa_groups` nesta primeira fatia: a sessão de um agente do Hub não tem
  `has_active_membership` no Tenant de origem, então não pode `SELECT` em `wa_groups` sob RLS atual
  — correto e esperado. Uma vinculação opcional `source_wa_group_id` fica para uma fatia futura,
  atrás de uma função `SECURITY DEFINER` estreita (devolve só o `id`, nunca `enabled`/nome/mensagens).

**`hub_group_tenant_links`** — vínculo N:N grupo↔Tenant:
- `id`, `hub_id`, `hub_group_id`, `tenant_id`, `role`
  (`consumer|supplier|operator|partner|observer`), `status (active|revoked)`, `assigned_by/at`,
  `revoked_by/at`, `version`.
- `UNIQUE (hub_group_id, tenant_id) WHERE status='active'` — **decisão D-06: 1 vínculo ativo por
  Tenant no MVP** (aprovada). Múltiplos papéis/serviço por Tenant fica para uma ADR futura se for
  pedido.
- FK composta `(hub_group_id, hub_id) → hub_group_catalog(id, hub_id)` — impede vincular um grupo de
  outro Hub.

**`hub_tenant_relationships`** — relação comercial B2B, independente de qualquer grupo:
- `id`, `hub_id`, `consumer_tenant_id`, `supplier_tenant_id`, `service_kind`, `service_reference`
  (texto opaco, sem dado de contrato sensível), `status (active|suspended|revoked)`,
  `valid_from/until`, `created_by`.
- `CHECK (consumer_tenant_id <> supplier_tenant_id)`.
- `UNIQUE (hub_id, consumer_tenant_id, supplier_tenant_id, service_kind) WHERE status='active'`.
- **Decisão D-07: pastas compartilhadas (`Alpha ↔ Beta`) são sempre derivadas desta tabela + dos
  vínculos ativos de ambos os lados — nunca uma coleção manual arbitrária** (aprovada). Se
  coleções manuais forem pedidas depois, é escopo novo com ADR própria.

Tags e preferências de layout (RF-07/`hub_group_layout_preferences` do pacote) **ficam fora do Gate
1** — não são necessárias para provar o caso de uso central (vínculo Alpha/Beta) e entram, se
precisarem, no Gate 2/3 sem exigir nova decisão de autorização.

### 3. RLS das tabelas novas

Nenhuma policy nova em `wa_groups`/`wa_group_messages`/`wa_group_archive_batches` — zero alteração.

Para as tabelas novas, FORCE ROW LEVEL SECURITY com predicados baseados em
`has_hub_manage_access(tenant_id, current_user_id(), 'groups', hub_id)`:
- `hub_group_catalog`: visível/editável só se o ator for membro do Hub **e**
  `has_hub_manage_access(source_tenant_id, ..., 'groups', hub_id)` — ou seja, só quem tem
  delegação de "organizar grupos" **na origem** vê aquele registro de catálogo.
- `hub_group_tenant_links`: visível/editável linha a linha só se
  `has_hub_manage_access(tenant_id, ..., 'groups', hub_id)` para o `tenant_id` **daquela linha** —
  um ator com acesso só a Alpha nunca vê a linha do vínculo com Beta (RF-13/SEC-013), mesmo que veja
  o `hub_group_catalog` de origem Alpha com vínculo a Beta.
- `hub_tenant_relationships`: visível só se `has_hub_manage_access` for verdadeiro **para os dois
  lados** (`consumer_tenant_id` E `supplier_tenant_id`) — do contrário nem a existência da relação
  aparece (SEC-002).
- Criar um vínculo (`INSERT` em `hub_group_tenant_links`) exige, na camada de aplicação (não só
  RLS), confirmar `has_hub_manage_access` tanto para o Tenant de origem do catálogo quanto para o
  Tenant de destino do vínculo — porque a verificação de FK no Postgres não respeita RLS da tabela
  referenciada, então a app precisa checar os dois lados explicitamente antes do `INSERT` (ponto de
  teste SEC-005/009 do plano de QA do pacote).

### 4. O que esta ADR explicitamente NÃO faz (herdado do pacote, confirmado sem conflito)

- Não altera `wa_groups.enabled`, não ativa ingestão, não lê `wa_group_messages`.
- Não cria Ticket/SLA/fila/roteamento a partir de um vínculo de grupo (ADR-0015 preservada).
- Não soma privilégios de dois Tenants numa mesma operação — `EffectiveAccessResolver` continua
  resolvendo exatamente 1 Tenant por requisição (`internal/hub/application/effective_access.go:28-30`).
  Um "resolvedor de contexto" (Gate futuro, separável) só escolhe **qual** Tenant usar quando há
  ambiguidade; nunca executa com os dois.
- Não introduz biblioteca de drag-and-drop nesta fase — **decisão: primeira fatia de UI (Gate 3) sem
  DnD, só menu/teclado** (aprovada). DnD é fatia separada, só depois de provar backend/permissões.
- ADR-0051 (superfície externa de leitura de grupos) não é reaproveitada aqui: é sobre app externa
  dentro de UM tenant (allowlist de canal), não sobre operador Hub cross-tenant — escopos diferentes,
  sem relação.

## Alternativas rejeitadas

- **Nova família de permissões isolada ("group_metadata.organize") sem relação com `management_scopes`**:
  rejeitada — duplicaria a maquinaria de validade/revogação/auditoria que `has_hub_manage_access` já
  tem e testou, contrariando a regra do projeto de não duplicar mecanismos existentes.
- **Abrir RLS de `wa_groups`/`wa_group_messages` para o Hub**: rejeitada — é exatamente a decisão de
  2026-10-09 que esta ADR preserva.
- **Tratar o mesmo `provider_group_id` em duas conexões como a mesma entidade automaticamente**:
  rejeitada (D-03) — `UNIQUE` sempre inclui `hub_id + source_tenant_id + source_channel_connection_id`;
  unificação só por ação manual futura, com prova de titularidade nos dois lados.
- **Pastas/coleções manuais arbitrárias sem relação comercial**: rejeitada para o MVP (D-07 aprovada).

## Consequências

- Um novo valor `'groups'` passa a ser aceito no `CHECK` de `management_scopes`
  (`hub_tenant_service_contracts`), ao lado de `channels`/`integrations`. Contratos existentes não
  são afetados (array vazio continua o padrão).
- Nenhum contrato existente ganha o escopo `'groups'` automaticamente — é um valor a mais na lista
  permitida, não uma concessão.
- Testes adversariais seguem o padrão `TestHubRLS_*` já usado em `internal/hub/adapters/rls_integration_test.go`.
- Rollout: feature flag `OMNIRA_GROUP_ORGANIZER_ENABLED=false` controla toda API/UI nova; o schema é
  aditivo e pode existir em produção com a flag desligada sem efeito observável.

## Sequência de gates (mantida do pacote, com os ajustes de escopo acima) — status final

Todos os 5 gates foram autorizados explicitamente pelo proprietário (Gates 1-3 um a um; Gates 4-5 em
bloco, em 2026-11-11) e estão implementados. Evidências completas em `docs/adr/0059-evidence-matrix.md`.

- **Gate 1** — commit `ddfcbf5`: migration 000185 (scope `'groups'` + `hub_group_catalog` +
  `hub_group_tenant_links` + `hub_tenant_relationships`, todas FORCE RLS), testes adversariais
  Postgres reais A/B/C. POSTGRES VERIFIED.
- **Gate 2** — commit `496e825`: migration 000186 (recibos de idempotência), API HTTP de
  catálogo/vínculos/relações, OpenAPI, auditoria em `audit_events`, feature flag
  `OMNIRA_GROUP_ORGANIZER_ENABLED` (OFF por padrão). HTTP VERIFIED.
- **Gate 3** — commit `4084921`: tela `/admin/grupos` (Lista/Pastas/Kanban, **sem** drag-and-drop —
  D-12), `can_manage_groups` em `GET /hubs`, checkbox de escopo no `CompaniesPanel`. E2E VERIFIED
  (Playwright real, prova explícita de ausência de `draggable`).
- **Gate 4** — commit `e3c3e87`: `GET /hubs/{hub_id}/groups/catalog/{group_id}/resolve-context`,
  read-only, 3 status (`resolved`/`ambiguous`/`no_access`), nunca soma dois tenants, não integrado a
  nenhum motor de ticket/SLA/fila. HTTP VERIFIED.
- **Gate 5** — hardening: removida uma duplicação evitável de `provider_group_id` (JID do WhatsApp) na
  metadata de auditoria do catálogo; métricas novas de cardinalidade fechada
  (`omnira_hub_group_organizer_writes_total`, `omnira_hub_group_organizer_resolve_context_total`);
  matriz de evidências consolidada; plano de rollback/rollout abaixo. POSTGRES VERIFIED.

### Plano de rollback

A flag já é suficiente: `OMNIRA_GROUP_ORGANIZER_ENABLED=false` (padrão atual em todos os ambientes)
remove as 10 rotas HTTP do organizador do roteamento (`internal/platform/httpserver/server.go`, bloco
`if groupOrganizerAPI`) sem exigir migração reversa — o schema é aditivo e fica inerte. Reverter uma
migração só seria necessário se o próprio schema precisasse ser desfeito (não é o caso para desligar a
feature): `tools/migrate-sql.sh down 000184_hub_group_organizer_writes` e, em seguida,
`down 000183_hub_group_organizer`, nessa ordem, já testados em Postgres descartável nos Gates 1 e 2.

### Plano de rollout faseado (documentação — nenhuma flag foi ligada em nenhum ambiente)

1. Ligar `OMNIRA_GROUP_ORGANIZER_ENABLED=true` em um ambiente de homologação primeiro, sem nenhum
   contrato real com `'groups'` em `management_scopes` ainda — valida que a flag OFF→ON não altera
   nenhum comportamento existente (rotas antigas, `/groups` do Tenant, Inbox, WAHA).
2. Conceder `'groups'` a UM contrato Hub↔Tenant piloto (via `CompaniesPanel`, checkbox "Organização de
   grupos") e registrar manualmente 1-2 grupos de teste no catálogo. Observar `audit_events` e as
   métricas novas por um período curto antes de qualquer expansão.
3. Expandir para outros contratos um a um, nunca em lote automático; cada concessão de escopo continua
   exigindo uma ação humana explícita no `CompaniesPanel` — não há ativação em massa.
4. Produção: só com autorização explícita e separada do proprietário, por fora desta ADR (regra
   permanente do projeto — "nunca ligar em produção sozinho"). Esta ADR entrega o mecanismo e o plano;
   não decide por si só quando executá-lo.
