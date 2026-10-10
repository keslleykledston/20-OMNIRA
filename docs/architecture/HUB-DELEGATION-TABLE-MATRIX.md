# Matriz de tabelas × acesso (Fase 01 do ADR-0040)

Gerada em 2026-10-09 a partir do catálogo REAL do banco (`pg_policies`, somente leitura). A coluna "quem" resume o predicado de cada comando:
`member` = membership ativa da instância (qualquer papel), `admin` = administrador da instância, `hub_read` / `hub_manage` = delegação do Hub já existente,
`self` = o próprio usuário, `sysadmin` = só sistema, `other` = expressão própria (revisada à mão).

**Achado estrutural:** nas tabelas operacionais o banco **não distingue papéis**: qualquer membro (inclusive `tenant_agent`) pode ler e escrever o conjunto inteiro.
A distinção por capacidade (`contact.classify`, `ticket.create`...) vive na aplicação (`PermissionChecker`). Hoje o RLS é a barreira de ISOLAMENTO ENTRE INSTÂNCIAS, não de privilégio.
Isso importa para o parecer A+: exigir capacidade fina no banco só para delegados seria mais rígido que para membros. Ver ADR-0040 §5.

Classe e domínio abaixo são **PROPOSTA** a revisar (gate da fase: nenhuma mudança generalizada de RLS antes desta revisão).

| Tabela | tenant_id | SELECT | INSERT | UPDATE | DELETE | ALL | classe proposta | domínio | nota |
|---|---|---|---|---|---|---|---|---|---|
| `follow_up_items` | T | member | member | member | member | — | operacional | chamado/tópico |  |
| `group_message_topic_links` | T | member | member | member | member | — | operacional | chamado/tópico |  |
| `message_topic_links` | T | member | member | member | member | — | operacional | chamado/tópico |  |
| `ticket_external_create_attempts` | T | member | member | member | member | — | operacional | chamado/tópico |  |
| `ticket_external_status_attempts` | T | member | member | member | member | — | operacional | chamado/tópico |  |
| `tickets` | T | member | member | member | member | — | operacional | chamado/tópico |  |
| `topic_account_links` | T | member | member | member | member | — | operacional | chamado/tópico |  |
| `topic_conversation_links` | T | member | member | member | member | — | operacional | chamado/tópico |  |
| `topic_entities` | T | member | member | member | member | — | operacional | chamado/tópico |  |
| `topic_group_links` | T | member | member | member | member | — | operacional | chamado/tópico |  |
| `topic_handoffs` | T | member | member | member | — | — | operacional | chamado/tópico |  |
| `topic_summaries` | T | member | member | member | member | — | operacional | chamado/tópico |  |
| `topic_threads` | T | member | member | member | member | — | operacional | chamado/tópico |  |
| `topic_ticket_links` | T | member | member | member | member | — | operacional | chamado/tópico |  |
| `account_external_links` | T | member | member | member | — | — | operacional | contato/cliente |  |
| `ambiguity_cases` | T | member | member | member | member | — | operacional | contato/cliente |  |
| `contact_account_links` | T | member | member | member | — | — | operacional | contato/cliente |  |
| `contact_notes` | T | member | member | member | member | — | operacional | contato/cliente |  |
| `contacts` | T | member | member | member | member | — | operacional | contato/cliente |  |
| `crm_contact_company_evidence` | T | member | member | member | member | — | operacional | contato/cliente |  |
| `customer_accounts` | T | member | member | member | — | — | operacional | contato/cliente |  |
| `identity_resolution_conflicts` | T | member | member | member | — | — | operacional | contato/cliente |  |
| `user_channel_identities` | T | member | member | member | — | — | operacional | contato/cliente |  |
| `assignment_events` | T | member | member | member | member | — | operacional | conversa |  |
| `channel_participants` | T | member | member | member | member | — | operacional | conversa |  |
| `conversation_channel_participants` | T | member | member | member | member | — | operacional | conversa |  |
| `conversation_closures` | T | member | member | member | member | — | operacional | conversa |  |
| `conversation_participants` | T | member | member | member | — | — | operacional | conversa |  |
| `conversation_topic_focus` | T | member | member | member | member | — | operacional | conversa |  |
| `conversations` | T | hub_read+member | member | member | member | — | operacional | conversa |  |
| `messages` | T | hub_read+member | member | member | member | — | operacional | conversa |  |
| `routing_decisions` | T | member | member | member | member | — | operacional | conversa |  |
| `agent_profiles` | T | member | member | member | — | — | operacional | conversa (diretório) | leitura para seletores/transferência; escrita só membro |
| `queue_members` | T | member | member | member | member | — | operacional | conversa (diretório) | leitura para seletores/transferência; escrita só membro |
| `queues` | T | member | member | member | member | — | operacional | conversa (diretório) | leitura para seletores/transferência; escrita só membro |
| `flow_node_executions` | T | member | member | member | member | — | operacional | fluxo (executar) | definição (flows, flow_versions, *_installations) NÃO é delegável: só executar fluxo aprovado |
| `flow_runs` | T | member | member | member | member | — | operacional | fluxo (executar) | definição (flows, flow_versions, *_installations) NÃO é delegável: só executar fluxo aprovado |
| `ai_tool_calls` | T | member | member | member | — | — | operacional | ia (assistência) | ai.assist; cada ferramenta confere a própria capacidade |
| `channel_message_templates` | T | member | member | member | member | — | operacional | mídia/mensagem | message_media_analysis é de sistema |
| `message_interactive_sends` | T | member | member | — | — | — | operacional | mídia/mensagem | message_media_analysis é de sistema |
| `message_media` | T | member | member | sysadmin | sysadmin | — | operacional | mídia/mensagem | message_media_analysis é de sistema |
| `message_outbound_media` | T | member | member | member | sysadmin | — | operacional | mídia/mensagem | message_media_analysis é de sistema |
| `message_template_sends` | T | member | member | — | — | — | operacional | mídia/mensagem | message_media_analysis é de sistema |
| `audit_events` | T | hub_manage+member | self | — | — | — | gestão delegada (já existe) | canais/integrações | políticas hub_manage; ver revisão |
| `channel_connections` | T | hub_manage+member | admin+hub_manage | admin+hub_manage | admin+hub_manage | — | gestão delegada (já existe) | canais/integrações | políticas hub_manage; ver revisão |
| `tenant_entitlements` | T | hub_manage+member | sysadmin | sysadmin | sysadmin | — | gestão delegada (já existe) | canais/integrações | políticas hub_manage; ver revisão |
| `ai_usage` | T | admin | sysadmin | — | — | — | administrativa | equipe/governança | nunca por delegação (tenants: leitura do nome já existe via hub_read) |
| `membership_invitations` | T | sysadmin | member_inline | sysadmin | — | — | administrativa | equipe/governança | nunca por delegação (tenants: leitura do nome já existe via hub_read) |
| `memberships` | T | member | admin | admin | — | — | administrativa | equipe/governança | nunca por delegação (tenants: leitura do nome já existe via hub_read) |
| `tenants` | - | hub_read+member | sysadmin | admin | — | — | administrativa | equipe/governança | nunca por delegação (tenants: leitura do nome já existe via hub_read) |
| `flow_pack_installations` | T | member | member | member | member | — | administrativa | fluxo (definição) | editar/publicar: só membro |
| `flow_template_installations` | T | member | member | member | member | — | administrativa | fluxo (definição) | editar/publicar: só membro |
| `flow_versions` | T | member | member | member | member | — | administrativa | fluxo (definição) | editar/publicar: só membro |
| `flows` | T | member | member | member | member | — | administrativa | fluxo (definição) | editar/publicar: só membro |
| `channel_credentials` | T | member+other | admin+hub_manage | admin+hub_manage | admin+hub_manage | — | segredo | credenciais/config sensível | ciphertext/chaves; ver revisão das 3 tabelas |
| `tenant_ai_integrations` | T | admin | admin | admin | admin | — | segredo | credenciais/config sensível | ciphertext/chaves; ver revisão das 3 tabelas |
| `wa_group_archive_batches` | T | member | member | member | member | — | só membro (decidido 2026-10-09) | grupos WhatsApp | fora da delegação por decisão do dono (2026-10-09) |
| `wa_group_messages` | T | member | member | member | member | — | só membro (decidido 2026-10-09) | grupos WhatsApp | fora da delegação por decisão do dono (2026-10-09) |
| `wa_groups` | T | member | member | member | member | — | só membro (decidido 2026-10-09) | grupos WhatsApp | fora da delegação por decisão do dono (2026-10-09) |
| `channel_webhook_events` | T | member | admin | — | — | — | sistema | infra/worker | só worker/sistema |
| `intelligence_jobs` | T | member | sysadmin | sysadmin | sysadmin | — | sistema | infra/worker | só worker/sistema |
| `message_media_analysis` | T | member | sysadmin | sysadmin | sysadmin | — | sistema | infra/worker | só worker/sistema |
| `outbox_events` | T | member | self | sysadmin | — | — | sistema | infra/worker | só worker/sistema |
| `agent_skills` | - | self | sysadmin | sysadmin | sysadmin | — | plataforma/Hub | relação e identidade | não pertencem à instância; sem mudança |
| `company_creation_requests` | - | self | sysadmin | sysadmin | sysadmin | — | plataforma/Hub | relação e identidade | não pertencem à instância; sem mudança |
| `effective_access_grants` | T | self | sysadmin | sysadmin | sysadmin | — | plataforma/Hub | relação e identidade | não pertencem à instância; sem mudança |
| `hub_inbox_items` | T | hub_read | sysadmin | sysadmin | sysadmin | — | plataforma/Hub | relação e identidade | não pertencem à instância; sem mudança |
| `hub_memberships` | - | self | sysadmin | sysadmin | sysadmin | — | plataforma/Hub | relação e identidade | não pertencem à instância; sem mudança |
| `hub_preauthorizations` | - | — | — | — | — | sysadmin | plataforma/Hub | relação e identidade | não pertencem à instância; sem mudança |
| `hub_tenant_service_contracts` | T | self | sysadmin | sysadmin | sysadmin | — | plataforma/Hub | relação e identidade | não pertencem à instância; sem mudança |
| `platform_operators` | - | self | sysadmin | sysadmin | sysadmin | — | plataforma/Hub | relação e identidade | não pertencem à instância; sem mudança |
| `service_hubs` | - | self | sysadmin | sysadmin | sysadmin | — | plataforma/Hub | relação e identidade | não pertencem à instância; sem mudança |
| `skills` | - | self | sysadmin | sysadmin | sysadmin | — | plataforma/Hub | relação e identidade | não pertencem à instância; sem mudança |
| `user_identities` | - | self | sysadmin | sysadmin | — | — | plataforma/Hub | relação e identidade | não pertencem à instância; sem mudança |
| `users` | - | other+self | sysadmin | self | — | — | plataforma/Hub | relação e identidade | não pertencem à instância; sem mudança |
| `work_pool_instances` | T | self | sysadmin | sysadmin | sysadmin | — | plataforma/Hub | relação e identidade | não pertencem à instância; sem mudança |
| `work_pool_members` | - | self | sysadmin | sysadmin | sysadmin | — | plataforma/Hub | relação e identidade | não pertencem à instância; sem mudança |
| `work_pools` | - | self | sysadmin | sysadmin | sysadmin | — | plataforma/Hub | relação e identidade | não pertencem à instância; sem mudança |
| `permissions` | - | other | — | — | — | — | catálogo global | papéis | leitura de catálogo |
| `role_permissions` | - | other | — | — | — | — | catálogo global | papéis | leitura de catálogo |
| `roles` | T | other | — | — | — | — | catálogo global | papéis | leitura de catálogo |

## Contagem por classe proposta

- operacional: 43
- gestão delegada (já existe): 3
- administrativa: 8
- segredo: 2
- só membro (decidido 2026-10-09): 3
- sistema: 4
- plataforma/Hub: 15
- catálogo global: 3

Total: 81 tabelas com RLS.


## Atualização (migration 111) — contexto preso a uma instância

Políticas restritivas `*_acting_one_instance` em `conversations`, `customer_accounts`, `contacts` e `contact_account_links`: ao agir por um Hub, só a instância travada por `lock_served_tenant` (`acting_tenant()`) é visível/escrevível. `messages` e a mídia herdam pela conversa. Ver ADR-0040 §14.

## Atualização (fase 04a, migration 110)

Políticas delegadas acrescentadas (todas permissivas, só valem no contexto delegado, por domínio; `delegated_tenants(domínio, necessidade)`):

| Tabela | SELECT | INSERT | UPDATE | DELETE | Observação |
|---|---|---|---|---|---|
| `contacts` | `contact`/leitura (+ conversa visível) — 109 | — | `contact`/escrita (+ conversa visível) | — | contato nunca é criado nem apagado por delegado |
| `contact_account_links` | `contact`/leitura (+ contato visível) | `contact`/escrita (+ contato visível) | `contact`/escrita (+ contato visível) | — | vínculo é encerrado, nunca apagado |
| `customer_accounts` | `contact`/leitura | — | — | — | só leitura; criar/editar conta é fase 04b |

Funções `SECURITY DEFINER` novas: `delegated_recompute_contact_kinds(tenant, contato)` e `delegated_dequeue_spam(tenant, contato)` (exigem contexto delegado **e** `contact.classify`; a segunda só age sobre contato `spam`).

Políticas **restritivas** acrescentadas na 110 (AND com as permissivas; só mudam algo no contexto delegado): `conversations_acting_hub_only` e `messages_acting_hub_only` — ao agir por um Hub, a conversa só é visível pelo
contrato e concessão DESSE Hub (escopo de fila incluído). Corrige a soma entre Hubs (Codex HIGH-1 da 04a).
