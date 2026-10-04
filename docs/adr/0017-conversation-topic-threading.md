# ADR-0017: Conversation, TopicThread e Ticket são conceitos distintos (Conversation Intelligence)

## Status
Accepted (2026-10-04). Implementação por ondas; as flags de automação nascem **desligadas**.

## Contexto
O modelo atual é `Contact -> Conversation -> Ticket`, com `tickets_active_conversation_uq` (um ticket ativo por conversa) e um
ticket-placeholder criado em toda conversa nova (`internal/inbox/application/inbound.go`). Isso funciona para atendimento individual,
mas quebra quando:
- **grupos** (ADR-0015) e conversas longas misturam vários assuntos ao mesmo tempo (pedido atrasado, nota errada, acesso bloqueado);
- o mesmo assunto atravessa **canais** (WhatsApp, e-mail) ou **conversas** (do grupo para o privado);
- o mesmo contato abre vários assuntos em paralelo, cada um com seu processo operacional.

Estado verificado no repositório em 2026-10-04 (HEAD `b5a71fc`): Go + Postgres 16 com RLS forçada; `contacts`, `conversations`,
`messages`, `tickets` com chaves compostas `(tenant_id, id)`; `conversation_participants` existe mas é **de agentes** (co-atendimento);
`outbox_events` + publisher NATS com entrega **at-least-once**; resumo por IA (`internal/ai`, PRODUCT.7C1) e mídia/IA multimodal
(ADR-0016: `message_media`, `message_media_analysis`, `tenant_ai_integrations`).

## Decisão
Separar três conceitos, sem reescrever os existentes:

| Conceito | Papel |
|---|---|
| **Conversation** | onde a mensagem aconteceu (transporte físico, linha do tempo física) |
| **TopicThread** | o assunto lógico em tratamento; pode atravessar conversas e canais |
| **Ticket** | o processo operacional/CRM ligado ao assunto |

Relações (todas por tabelas de ligação, todas `tenant_id`-escopadas com FK composta):
- `message_topic_links`: uma mensagem pode ter 0, 1 ou vários tópicos (`primary`, `secondary`, `supporting`, `ambiguous`);
- `topic_conversation_links`: um tópico atravessa várias conversas;
- `topic_ticket_links`: N:N entre tópicos e tickets;
- `topic_summaries`: resumos **versionados** (nunca um campo sobrescrito);
- `channel_participants`: participante **externo** do canal (não confundir com `conversation_participants`, que é de agentes).

Princípios:
1. **A IA não é autoridade.** Ela propõe; o OMNIRA decide por política determinística, permissões, tenancy e auditoria.
2. **Evidência forte vence limiar** (resposta direta, token de handoff, entidade exata); IA só entra depois do roteador determinístico,
   primeiro em **modo sombra** (grava a proposta, não muda associação).
3. **Todo conteúdo do cliente é não confiável** (mensagem, transcrição, PDF, OCR): nunca altera instruções, permissões, tenant,
   provedor, políticas de ferramenta nem aprovação. O contexto separa `SYSTEM POLICY`, `TRUSTED SYSTEM DATA` e `UNTRUSTED CUSTOMER CONTENT`.
4. **Intelligence nunca é ponto único de falha**: ingestão, resposta, ticket manual e tópico manual funcionam com o worker desligado.
5. **Consumidores idempotentes** (a outbox entrega pelo menos uma vez): constraints únicas + claim com `FOR UPDATE SKIP LOCKED`.

## Consequências
- Roteamento de mensagens (determinístico, depois IA) e estado explícito de **ambiguidade** com resolução humana/cliente.
- Ticket passa a **N:N** com tópico; durante a migração os tickets atuais continuam **escopados à conversa** e ganham um tópico legado
  (`source = legacy_backfill`) apenas quando necessário. Nenhum backfill em massa classifica o histórico.
- O construtor de contexto passa a ser **ciente do tópico** (resumo confirmado > inferido, entidades, tickets, mídia), com níveis de verdade
  (`system_verified > provider_verified > customer_confirmed > agent_confirmed > ai_inferred`); a IA não sobrescreve os de nível superior.
- Mesmo contato com vários tópicos ativos; mesmo tópico atravessando canais (correlação por entidade, nunca com baixa confiança).
- Novo bounded context `internal/intelligence/` (domain, ports, application, adapters). O Inbox segue dono das mensagens e o módulo de
  tickets dono do ticketing; Intelligence interpreta e correlaciona.
- Permissões novas `topic.read` e `topic.manage` (matriz fixa de papéis, como `group.*`). Mutação em tópicos de uma conversa exige
  `topic.manage` **e** ser o atendente da conversa ou ter `conversation.manage` (mesma regra do envio de mensagens).

## Compatibilidade retroativa
Tabelas e rotas atuais não mudam de significado. `tickets.conversation_id` e o índice único de ticket ativo por conversa permanecem.
Tudo novo é aditivo e protegido por flags (`topic_threads_enabled` ligada; `topic_auto_routing_enabled`, `topic_ai_routing_enabled`,
`auto_ticket_policy_enabled` e as demais desligadas).

## Alternativas rejeitadas
- `messages.topic_id` único: impede multi-tópico e grupos.
- `contacts.active_ticket_id`: um contato tem vários assuntos simultâneos.
- `conversations.ticket_id` como autoridade: repete o erro atual e impede assunto que atravessa conversas.
- `Conversation = Ticket`: mistura transporte com processo operacional.
- Auto-merge de tópicos por similaridade/embedding: irreversível e opaco; merge é ação humana, preservando o histórico.
- Chamar a IA de dentro do webhook: acopla disponibilidade da ingestão ao provedor; a IA roda em worker, por evento.

## Ondas
1 fundação de tópicos (sem IA) -> 2 participantes externos e grupos -> 3 entidades + auditoria + roteador determinístico ->
4 pipeline durável de eventos -> 5 contexto e resumo -> 6 roteador de modelos e IA em modo sombra -> 7 política de ticket ->
8 handoff privado -> 9 multimodal (reaproveita ADR-0016) -> 10 contabilização de uso de IA -> 11 copiloto e superfície web ->
12 gateway de ferramentas. Cada onda só é commitada com os testes verdes; nada vai para produção sem autorização explícita.
