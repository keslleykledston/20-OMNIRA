# Modelo de Dados Conceitual

## Núcleo

```text
User
Membership
Tenant
Hub
HubTenantGrant
Role
Permission

Contact
ChannelConnection
Conversation
Message
Ticket
Queue
Department
Assignment
SlaPolicy

IntegrationConnection
ExternalCustomerRef
ExternalTicketRef

AutomationFlow
AutomationVersion
AutomationRun

AuditEvent
DomainEvent
```

## Invariantes

- `Contact.tenant_id` obrigatório.
- `Conversation.tenant_id` obrigatório.
- `Ticket.tenant_id` obrigatório.
- `Message.tenant_id` derivado e persistido para enforcement/particionamento.
- `Queue`, `Department`, `ChannelConnection`, `IntegrationConnection` pertencem a um Tenant.
- `HubTenantGrant` concede acesso, não move dados.
- FKs tenant-owned devem preferir validação composta `(tenant_id, id)` quando aplicável.

## IDs

Usar UUID/ULID internos. CNPJ, telefone, ids de ERP e ids de provedor são atributos/identificadores externos, nunca chave primária de autorização.

## Conversation Intelligence: tópicos (ADR-0017, migration 000063)
`Conversation` é onde a mensagem aconteceu; `TopicThread` é o assunto lógico; `Ticket` é o processo operacional. Tabelas (todas com `tenant_id`,
RLS forçada e FK composta `(tenant_id, id)`):

| Tabela | Papel |
|---|---|
| `topic_threads` | o assunto (`status` open/resolved/archived; `privacy_policy`; `source` manual/rule/ai/handoff/legacy_backfill) |
| `message_topic_links` | mensagem ↔ tópico, N:N (`relation` primary/secondary/supporting/ambiguous; `decision_source`; `confidence`) |
| `topic_conversation_links` | tópico ↔ conversa (um tópico atravessa conversas e canais) |
| `topic_ticket_links` | tópico ↔ ticket, N:N; no máximo **um** `primary` por tópico |
| `topic_summaries` | resumos versionados (`UNIQUE (tenant, topic, version)`); nunca sobrescrito |

Regras: um vínculo de mensagem só é substituído por uma decisão de **autoridade igual ou maior** (agent/customer > explicit/handoff/reply > entity >
rule > ai > legacy), então uma decisão posterior ou repetida da IA nunca desfaz a de uma pessoa. Tickets atuais continuam escopados à conversa
(`tickets_active_conversation_uq` intocado). Permissões novas: `topic.read` e `topic.manage` (admin, supervisor, agente); alterar tópicos exige também ser o
atendente da conversa ou ter `conversation.manage`.
Flags (`internal/intelligence/application/flags.go`, variáveis `OMNIRA_*`): só `topic_threads_enabled` nasce ligada.
