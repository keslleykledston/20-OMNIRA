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

### Participantes externos e respostas (ADR-0017, migration 000064)
- `channel_participants`: quem escreveu, **nos termos do provedor** (WhatsApp `@lid`/`@c.us`, `wa_id` da Meta), qualificado por tenant, conexão e provedor
  (o mesmo id em outra conexão ou outro tenant é outro participante). Opcionalmente ligado a um `contact`; a primeira ligação vence e o nome de exibição
  é só rótulo. **Não** é `conversation_participants` (que são agentes do OMNIRA em co-atendimento).
- `conversation_channel_participants`: o participante numa conversa (`customer` em 1:1).
- Metadados de mensagem: `messages.sender_channel_participant_id`, `reply_to_external_message_id` (o que o provedor disse) e `reply_to_message_id` (só quando resolve
  **dentro da mesma conversa**); o mesmo em `wa_group_messages` (resolve dentro do mesmo grupo). "Citada" e "resposta" são a mesma relação (WAHA `replyTo`, Meta
  `context`), então há um só conjunto de colunas. WAHA informa o id curto (3º segmento do id serializado); a Meta, o `wamid` completo: ambos casam.
- Não observado nos payloads reais, portanto **não** modelado: menções.
