# ADR-0020: Finalizar atendimento e memória do contato entre atendimentos

## Status
**Aceito e implementado em `main` (2026-10-06), ainda NÃO implantado.** Ondas W0 a W3c concluídas (ver "Plano"). O núcleo (finalizar, histórico,
pendências) é aditivo e guiado por permissões existentes; as partes de IA (sugestão de resumo e memória no copiloto) nascem **desligadas** por
flag. **Adiado, com motivo:** a resolução automática de tópicos por evento (seção 5), o fechamento em lote das conversas legadas, a
finalização por inatividade e o nó `fechar conversa` do Flow Builder.

## Contexto
Estado verificado no repositório e no banco de desenvolvimento em 2026-10-06 (HEAD `f42408b`):

- `conversations.status` só admite `open` e `closed`; o método de domínio `Conversation.Close()` existe, mas **nenhuma rota, caso de uso
  ou tela o chama**. Hoje há 156 conversas abertas e nenhuma fechada (duas foram fechadas por SQL, por ordem do dono, para um teste).
- O ingest reaproveita a conversa **enquanto ela estiver aberta** (`FindOpen(contato, linha)`). Sem fechar, depois da primeira conversa
  de um contato naquela linha todas as mensagens entram na mesma conversa para sempre.
- Consequências: o roteamento (fila, round-robin) só roda na primeira mensagem do contato; o Flow Builder (ADR-0019) com
  `restart_policy=new_conversation_only` só atua uma vez por contato; o ticket-placeholder nunca é encerrado e infla os "tickets
  abertos"; o atendente não tem um marcador de "este assunto acabou, estou livre para o próximo".
- O ADR-0017 já separa `Conversation` (transporte), `TopicThread` (assunto, que pode atravessar conversas) e `Ticket` (processo). O
  copiloto e o construtor de contexto são **escopados ao tópico**: material de outro tópico nunca entra no contexto. Existe um
  gateway de ferramentas fechado (`ToolRegistry`) com ferramentas de leitura/escrita executadas com as permissões de quem pede.
- O pedido do dono: o copiloto deve poder buscar contexto em atendimentos anteriores para enriquecer a conversa e dar ao atendente
  ferramentas para saber do que se trata, o que foi tratado, **o que ficou pendente e o que foi prometido**.

## Decisão

### 1. "Finalizar atendimento" encerra o **episódio de atendimento**, não o chat do WhatsApp
Finalizar fecha a `Conversation` (`status=closed`, `closed_at`). A mensagem seguinte do mesmo contato na mesma linha cria uma
**conversa nova** (já é o comportamento do ingest quando não há conversa aberta): ela volta a passar pelo roteamento, pelo ticket
inicial e pelo gate do Flow Builder, e herda o contexto do atendimento anterior pelo histórico do contato (seção 3), não por reabrir.
Reabrir não faz parte desta decisão (decisão consciente: uma conversa fechada é final; reabrir seria uma ADR própria).

Em **uma única transação** com a mesma trava de linha que o motor de fluxos usa (`SELECT … FOR UPDATE` na conversa):
1. autoriza (seção 4) e confere que a conversa é de um contato (conversa interna, ADR-0018, não se finaliza);
2. se já estiver fechada, devolve o fechamento existente (idempotente, 200 com `changed=false`);
3. grava o registro de fechamento (`conversation_closures`) e as pendências informadas (`follow_up_items`);
4. fecha os **tickets locais** da conversa (`external_ticket_id IS NULL` **e** `NOT topic_scoped`). Ticket ligado ao ERP **nunca** é fechado
   por aqui (o ERP é a autoridade, ADR-0013), nem ticket escopado a um assunto (pode atravessar conversas, ADR-0017): o fechamento
   registra quantos ficaram abertos (`tickets_kept`) e a tela avisa. (O índice `tickets_active_conversation_uq` admite um único ticket
   ativo não escopado por conversa, então o caso comum é um só.)
5. `automation_mode='none'` (um bot não pode reter conversa fechada); a execução de fluxo em andamento é cancelada pelo caminho que
   já existe (o motor ignora/cancela runs de conversa fechada e o sweeper de 15 s também);
6. grava auditoria (`conversation.closed`: ids, enums e contagens, nunca o texto do resumo). O evento de outbox `conversation.closed.v1`
   (**só ids**) é emitido na onda W3, junto do consumidor que o usa, para não deixar evento sem destino;
7. o gatilho de realtime já existente (`conversations_realtime_trg`, `UPDATE OF status`) avisa a interface.

A atribuição (`assigned_to_user_id`) é mantida como histórico; a capacidade do atendente é liberada porque só conversas abertas contam.

### 2. Registro de fechamento e pendências são dados próprios (migration `000086`, aditiva)
- `conversation_closures` (1 por conversa; **imutável**: sem UPDATE/DELETE para `omnira_app` e com gatilho contra `UPDATE`, ambos pela migration `000087`, porque a `000006` concede DML a toda tabela nova por privilégio padrão): motivo (`resolved`, `no_response`,
  `duplicate`, `spam`, `transferred`, `other`), nota, **resumo do atendimento** com nível de verdade (`agent_confirmed` ou
  `ai_inferred`, mesma escala do ADR-0017), contagem de tickets locais fechados e de externos mantidos, quem fechou e a origem
  (`agent`, `supervisor`, `system`).
- `follow_up_items` (por **contato**, sobrevive à conversa): tipo `pending` (algo a fazer), `promise` (algo prometido ao cliente) ou
  `info` (fato a lembrar); texto (≤ 500), responsável opcional, prazo opcional, estado `open`/`done`/`dropped` com quem/quando
  resolveu, e o nível de verdade. A IA só **propõe** (`ai_inferred`); a pessoa confirma ao finalizar, e só então vira
  `agent_confirmed`. Uma pendência da IA não confirmada nunca vale como fato.
- Ambas com RLS `ENABLE` + `FORCE`, chaves compostas `(tenant_id, …)` e política por `has_active_membership`, como as demais.

### 3. O histórico e as pendências do contato são lidos na conversa seguinte
O painel de contexto do Inbox ganha **Atendimentos anteriores** (resumos e motivos) e **Pendências do contato** (abertas, com prazo e
responsável, com ação de concluir/descartar). Qualquer membro ativo lê (mesma visibilidade do Inbox); concluir/descartar exige a
mesma permissão de operar a conversa.

### 4. Autorização: sem permissão nova
Mesma regra do `Unassign`: precisa de `conversation.claim` ou `conversation.manage`; quem **não** é o responsável atual só pode
finalizar com `conversation.manage`; conversa **sem responsável** (fila ou bot) só com `conversation.manage`. Papéis padrão:
`tenant_admin` e `tenant_supervisor` têm `manage`; `tenant_agent` finaliza as suas. Tenant vem do `TenantContext`, nunca do payload.

### 5. A inteligência reage por evento: ADIADO
A ideia original era um consumidor idempotente de `conversation.closed.v1` que resolvesse os tópicos ligados **somente** a essa conversa.
Foi adiado porque (a) exige infraestrutura nova de entrega (assunto no JetStream, consumidor no worker) cujo valor é pequeno hoje: um
tópico aberto numa conversa finalizada só aparece no painel de assuntos dessa conversa, sem efeito operacional; e (b) resolver tópico tem
regras do ADR-0017 (`topic.manage`, privacidade, resumos) que não devem ser contornadas por SQL. Quando for feito, deve usar o
`TopicService` e este evento. Até lá, finalizar funciona igual e os tópicos ficam como estão.

### 6. Memória do contato para o copiloto e para o atendente (a abertura controlada do escopo por tópico)
O ADR-0017 diz que o contexto do copiloto contém **somente** material do tópico atual. Esta decisão abre **uma** exceção, estreita e
auditável: o **mesmo contato**, nunca outro, nunca outro tenant.

a) **Enriquecimento passivo** (flag `OMNIRA_COPILOT_CONTACT_MEMORY_ENABLED`, desligada, e só vale com `OMNIRA_COPILOT_ENABLED`): o construtor de contexto acrescenta uma seção
   *Memória do contato*, limitada (N últimos fechamentos com resumo + pendências abertas), com o nível de verdade de cada item e
   marcada como dado possivelmente desatualizado. Texto do cliente e resumos de IA continuam sendo tratados como **conteúdo não
   confiável** (`internal/platform/untrusted`): nunca viram instrução.

b) **Ferramentas de leitura** no gateway fechado (`ToolRegistry`, agora com 8 ferramentas: 5 de assunto + estas 3), executadas com as
   permissões de quem pede (`topic.read`):
   - `contact.recent_attendances`: últimos fechamentos do contato (resumo, motivo, data);
   - `contact.open_followups`: pendências e promessas abertas;
   - `contact.search_history`: busca por trecho nas mensagens das **conversas anteriores do mesmo contato**, com limite de resultados,
     de tamanho e de argumento.

   O contato é **derivado no servidor** a partir do tópico da chamada; os argumentos são estritos (`DisallowUnknownFields`), então um
   `contact_id`, `tenant_id` ou SQL contrabandeado é erro. O contato vem de `topic_threads.primary_contact_id`; um assunto sem contato
   principal (grupo) não tem memória e a ferramenta falha limpo. A busca deixa de fora as mensagens que o próprio assunto já carrega.
   Resultado limitado a 12 KiB (resumos cortados em 600 caracteres), registrado em `ai_tool_calls`.

c) **Sugestão ao finalizar** (`POST …/finalize/suggest`; roda na flag do copiloto e na tarefa de modelo `closing_suggest`, variável
   `OMNIRA_AI_MODEL_CLOSING_SUGGEST`; só leitura e **sem trava de linha**, porque a chamada ao modelo é lenta): a tela "Finalizar" pode pedir à IA um rascunho de resumo e de pendências
   (inclusive "o que foi prometido") a partir da conversa; o rascunho é `ai_inferred`, editável, e nada é gravado antes de a pessoa
   confirmar.

### 7. Efeitos no Inbox, descobertos na implementação
Uma conversa finalizada deixa de ser fila de trabalho: a lista do Inbox ganhou `status=open` (padrão) | `closed` | `all` e a aba
**Encerradas**; o envio de resposta a uma conversa finalizada é recusado (409 `conversation is finalized`), porque a resposta do cliente abriria
um atendimento novo e o contexto se dividiria; o chat mostra um aviso no lugar do campo de resposta.

### 8. Dados legados e inatividade ficam fora desta decisão
As 156 conversas abertas **não** são fechadas automaticamente. Ficam para etapas futuras, cada uma com ordem explícita e simulação
prévia: ferramenta administrativa de fechamento em lote (com `--dry-run`), finalização automática por inatividade (primeiro só em modo
aviso) e o nó `fechar conversa` do Flow Builder. A janela de 24 h da Meta é independente: continua contada da última mensagem do
cliente, e uma conversa nova com mensagem do cliente abre janela nova.

## Consequências
- O Inbox passa a ter um fim de atendimento e um começo limpo para o próximo assunto; o bot e o roteamento voltam a atuar em cada
  novo atendimento do mesmo contato; o painel de "tickets abertos" deixa de crescer sem fim (para tickets locais).
- Surge dado novo sensível (resumos e pendências em texto livre): mesma proteção de tenant (RLS), sem segredo em texto (a interface
  avisa, a API rejeita padrões de credencial pelo mesmo detector do Flow Builder) e sem envio a provedores de IA fora das flags.
- O copiloto ganha memória entre atendimentos sem perder o princípio central: **a IA não é autoridade**; ela lê e propõe.
- Risco aceito: busca por trecho com `ILIKE` no mesmo contato é suficiente no volume atual; índice de texto fica para medição.

## Alternativas descartadas
- **Fechar o ticket em vez da conversa**: não resolve o ingest (a conversa continua aberta) nem o bot/roteamento.
- **Reabrir conversa fechada em nova mensagem**: mistura episódios e o resumo do anterior perde sentido; mantém o bot mudo.
- **Deixar a IA gravar pendências sozinha**: viola "a IA não é autoridade"; uma promessa inventada vira compromisso falso.
- **Memória global do tenant para o copiloto**: vazamento entre clientes; só o mesmo contato.

## Compatibilidade retroativa
Tabelas e rotas existentes não mudam de significado. `tickets.conversation_id`, o ingest e o roteamento permanecem. Migration
`000086` só adiciona tabelas e a `000087` restringe privilégios das tabelas append-only (ambas reversíveis). Sem permissão nova, portanto a matriz fixa de papéis (`internal/iam3`) não muda.

## Plano
| Onda | Entrega | Verificação |
|---|---|---|
| W0 | Este ADR + índice | revisão |
| W1 (feito) | Migration 000086; serviço `Finalize`; rotas (finalizar, contexto da conversa, histórico do contato, resolver pendência); auditoria; contrato OpenAPI (`attendance-v1.yaml`) | Postgres real: tenant A/B, autorização, idempotência, concorrência com o bot, ticket local x externo, ingest cria conversa nova após fechar |
| W2 (feito) | Interface: botão e diálogo "Finalizar", painéis "Atendimentos anteriores" e "Pendências"; testes e Chromium real | vitest + Playwright |
| W3 (feito; consumidor de evento adiado) | Inteligência: busca de histórico; ferramentas `contact.*`; seção *Memória do contato*; sugestão ao finalizar | injeção de prompt, escopo por contato, limites, ledger |
| W4 (feito) | Docs, changelog, handoff, backup; gate | suíte completa |

Para implantar: aplicar a migration `000086` (com backup), reconstruir `api` e `web` (o `worker` não é afetado) e só então decidir as flags de IA.
Nada é implantado sem ordem explícita do dono.
