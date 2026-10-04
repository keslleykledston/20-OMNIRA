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

### Entidades, roteamento e ambiguidade (ADR-0017, migration 000065)
- `topic_entities`: assuntos nomeados (pedido, nota, contrato, chamado, equipamento...) com chave canônica; só tipos **genéricos**.
- Grupos do WhatsApp ficam em `wa_group_messages` (ADR-0015), então o lado de grupo tem `group_message_topic_links` e `topic_group_links`; uma mensagem roteável
  é de conversa **ou** de grupo (`CHECK` de um dos dois).
- `routing_decisions`: toda decisão com a evidência (`signals`), aplicada (`applied = true`) ou só proposta (flag desligada / modo sombra). Índices únicos
  garantem **uma** decisão ativa por mensagem (reentrega nunca duplica) e uma proposta por mensagem e fonte.
- `ambiguity_cases`: mensagem que o roteador não pôde colocar com confiança; uma pessoa (ou o cliente) resolve. `conversation_topic_focus`: **dica**, nunca autoridade.
- Roteador (`internal/intelligence/domain/routing.go`), sem LLM, evidência por ordem de autoridade: token de handoff, escolha explícita, resposta direta/citação, entidade
  exata (chamado, pedido, nota...), mesmo autor continuando (15 min), foco, tópico mais recente, palavras. Limiares e pesos em `RoutingConfig` (auto >= 0,85;
  ambíguo 0,60-0,85; abaixo disso, tópico novo se a mensagem nomeia um assunto, senão sem tópico). Uma entidade nova **enfraquece** as dicas de contexto
  (`NewSubjectPenalty`). Duas entidades de tópicos diferentes na mesma mensagem = multi-tópico (dois vínculos, mensagem não duplicada).
- Com `topic_auto_routing_enabled` desligada o roteador só **registra** a proposta; ligada, aplica. Uma mensagem já colocada (por pessoa, handoff ou decisão anterior)
  nunca é decidida de novo; uma edição humana marca a decisão automática como substituída (`overridden_at`).

### Contexto e resumos de tópico (ADR-0017, onda 5; sem migration nova)
- `topic_summaries` é **append-only por versão**: gerar, corrigir ou confirmar nunca reescreve texto. Estados: `ai_inferred` → `agent_confirmed`/`customer_confirmed`; `corrected` (texto do atendente, nova versão); `superseded` (substituído, continua legível). Um resumo da IA nunca toca um `corrected` nem rebaixa uma confirmação do cliente.
- O contexto de IA (`TopicContext`) só lê o que está **ligado ao tópico** (`message_topic_links`, `group_message_topic_links`). Participantes viram aliases (`Participante N`, `CLIENTE`, `ATENDENTE`); nome real, telefone e id do provedor nunca vão ao modelo. Zonas: política fixa (Instructions) / dados confiáveis do sistema / conteúdo não confiável (cada linha JSON-quoted, cercada por nonce aleatório por requisição).
- Geração é idempotente e serializada por tópico (advisory lock); passo opcional do worker (`OMNIRA_TOPIC_SUMMARIES_ENABLED`, desligado) — falha do provedor nunca falha o job.
- API: `GET /topics/{id}/summaries`, `POST /topics/{id}/summary/{generate,confirm,correct}` (leitura `topic.read`; escrita `topic.manage` + atendente/`conversation.manage`).

### Roteador de modelos e IA em sombra (ADR-0017, onda 6; sem migration nova)
- `ModelRouter` escolhe provedor/modelo/limites por tarefa (`topic_classify`, `topic_summary`); sem rota = tarefa indisponível (degrada para o roteador determinístico e para o trabalho manual). Modelo por tarefa: `OMNIRA_AI_MODEL_TOPIC_CLASSIFY` / `OMNIRA_AI_MODEL_TOPIC_SUMMARY` (padrão `OMNIRA_AI_MODEL`).
- `TopicClassifier` (flag `OMNIRA_TOPIC_AI_ROUTING_ENABLED`, desligada) **só propõe**: grava em `routing_decisions` com `decision_source='ai'`, `applied=false`, modelo, versão do prompt, latência e confiança. Nunca liga mensagem, cria tópico ou ambiguidade. O modelo vê aliases `T1..Tn` dos tópicos ABERTOS do mesmo tenant/conversa (nunca ids); a resposta é validada estritamente (objeto JSON único, campos conhecidos, alias dentro da whitelist, confiança 0..1) e qualquer desvio é descartado. Uma vez por mensagem (replay não chama o provedor). Falha/timeout do provedor = sem proposta, job segue.
- Métrica: `topic_ai_shadow_total{outcome}`.

### Política de ticket por tópico (ADR-0017, onda 7; sem migration nova)
- Invariante preservada: **um ticket ativo por conversa** (`tickets_active_conversation_uq`). A política (`DecideTicket`, determinística) nunca abre um segundo ticket simultâneo na mesma conversa nem tira o ticket de outro tópico: `adopt_active` (o ticket ativo sem dono vira primário do tópico), `create` (só quando a conversa não tem ticket ativo; usa o mesmo `TicketStore` do inbox), `share_active` (relação `related`, escolha explícita da pessoa) e `needs_agent` (grupo sem conversa, tópico em várias conversas, ticket ativo já pertence a outro tópico).
- `GET /topics/{id}/ticket-policy` é só conselho; `POST .../ticket-policy/apply` reavalia sob lock por conversa e recusa (409) o que a política não permite agora; `create` exige também `ticket.create`.
- Automação (`OMNIRA_AUTO_TICKET_POLICY_ENABLED`, desligada): só `adopt_active` e `create`, origem `rule`, falha nunca derruba o job.
- Backfill legado sob demanda: `POST /inbox/conversations/{id}/legacy-topic` cria um tópico `legacy_backfill` para o ticket ativo sem vínculo; não classifica histórico; idempotente.

### Handoff privado (ADR-0017, onda 8, migration 000067)
- `topic_handoffs`: convite curto e de uso único para continuar um assunto (tipicamente nascido num grupo) no chat privado. O token (`omn-` + 256 bits) é mostrado **uma vez** na criação; só o SHA-256 é gravado; nunca é logado nem listado. Validade padrão 24 h (máx. 72 h), até 3 pendentes por tópico, sem DELETE (revoga ou expira; `expired` é derivado).
- Resgate: passo do pipeline **antes do roteamento** (flag `OMNIRA_PRIVATE_HANDOFF_ENABLED`, desligada) em mensagem privada de entrada. Um único `UPDATE ... WHERE status='pending' AND expires_at>now() AND tópico aberto ... RETURNING` garante uso único sob concorrência; liga mensagem (decisão `handoff`, a evidência mais forte) e a conversa ao tópico. Token inválido/expirado/revogado/de outro tenant/em grupo: nada acontece e nada é revelado. Não há fusão automática de identidade do contato (pendência K3G, PRODUCT.7B).
- Métrica: `topic_handoff_redemptions_total{outcome}`. API: `POST|GET /topics/{id}/handoffs`, `POST .../handoffs/{id}/revoke`.

### Multimodal (ADR-0017, onda 9; sem migration nova)
- Reaproveita `message_media`/`message_media_analysis` (ADR-0016). `EnqueueVision` cria `description`/`document_text` (engine `gemini`) só para tenants com integração **habilitada** (a tabela exige chave + consentimento) e arquivos limpos, recentes e de mime permitido. `VisionProcessor` exige `UsageLedger` (sem ledger não há chamada externa); fiação no worker na onda 10.

### Contabilização de uso de IA (ADR-0017, onda 10, migration 000068)
- `ai_usage`: **uma linha por chamada** a modelo externo (sucesso ou não), append-only (sem UPDATE/DELETE; só o sistema grava; leitura só do administrador do tenant). Guarda provider/modelo/tarefa, tokens reportados e custo estimado (`NULL` quando o provedor é pago pela plataforma e não há preço: tokens ficam, nunca vira "grátis").
- Orçamento mensal (UTC) conta **só o provedor da chave do próprio tenant (Gemini)**, incluindo chamadas que falharam (podem ter sido cobradas); é checado **antes** de cada chamada de visão contra o pior caso dela. Classificação e resumo de tópico (OpenAI da plataforma) são registrados mas nunca bloqueiam o tenant.
- `GET /integrations/ai/usage?month=YYYY-MM` (tenant.manage). `GenerateResponse` ganhou contadores de tokens (só para contabilidade). O `VisionProcessor` agora sobe no worker com `OMNIRA_MULTIMODAL_ANALYSIS_ENABLED=true` (desligada por padrão).

### Copiloto de resposta (ADR-0017, onda 11 backend; sem migration nova)
- `POST /topics/{id}/copilot/suggest-reply` (flag `OMNIRA_COPILOT_ENABLED`, desligada; `topic.manage` + atendente): rascunho para a **última mensagem do cliente** do tópico, a partir do contexto do tópico (só dele, aliases, zonas confiável/não confiável). **Só sugere**: não há caminho do copiloto ao envio de mensagens (`sent` é sempre `false`; o atendente edita e envia pelo endpoint normal).
- Saída do modelo validada estritamente (JSON único `reply`/`missing_info`/`needs_human`); avisos **determinísticos** calculados pelo OMNIRA (link/e-mail/telefone/número fora do contexto, valor em R$, "já fiz", promessa). Falha do provedor/saída inválida → 503; limite de 1 chamada a cada ~4 s por tópico (429); cada chamada vai ao ledger. Modelo próprio via `OMNIRA_AI_MODEL_COPILOT`.

### Gateway de ferramentas com política (ADR-0017, onda 12, migration 000069)
- Registro **fechado** em código (não por tenant) de ferramentas **reais**: `topic.get_summary`, `topic.list_tickets`, `topic.ticket_policy_advice` (leitura) e `topic.generate_summary`, `topic.apply_ticket_policy` (escrita baixa). Não existe ferramenta para enviar mensagem, fechar chamado, SQL, script ou URL arbitrária; ferramenta sem executor ligado nem aparece no catálogo.
- Toda ferramenta roda com as **permissões do usuário que pediu** (o gateway nunca amplia autoridade). Argumentos estritos (campo desconhecido, como `tenant_id`/`url`/`sql`, é erro e não grava nada). Política: leitura executa; escrita pedida pela IA (`proposed_by=ai`, padrão) vira `pending_approval` e só roda quando uma pessoa autorizada aprova (executa com a autoridade do aprovador; um único aprovador vence); a mesma escrita pedida por uma pessoa é a própria pessoa agindo.
- `ai_tool_calls` (append-only, sem DELETE): uma linha por pedido, inclusive negados; chave de idempotência por tenant (retry devolve o primeiro resultado e age uma vez; mesma chave com pedido diferente = 409). Resultado é dado **não confiável**; erros viram motivo fixo. Vínculo de chamado criado via IA registra origem `ai`.
- Flag `OMNIRA_AI_TOOL_GATEWAY_ENABLED` (desligada). Hoje nenhum fluxo de IA chama o gateway sozinho: ele é a única porta preparada para isso e já é usável com aprovação humana pela API.

### Pipeline durável (ADR-0017, migration 000066)
`intelligence_jobs` (uma linha por mensagem e `pipeline_version`; estados pending/running/completed/failed/dead; lease em `locked_until`; só o sistema escreve) e os
gatilhos que emitem `job.inbox.message_persisted.v1` na mesma transação da mensagem. Ver `docs/EVENTS.md`.
