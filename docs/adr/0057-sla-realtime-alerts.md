# ADR-0057: Alertas de SLA em tempo real (toast, broadcast ao tenant)

## Status
Aceito (2026-11-10), a pedido do dono do produto, no fio da conversa sobre "colocar o projeto de grupos em
produção pra permitir agrupar, monitorar, alertar". Ao investigar o que "alertar" deveria significar, achamos que
**o vínculo grupo→cliente final já existe** (migração 000179, `GroupAccountLink`) e que **o alerta de verdade não é
uma funcionalidade de grupo**: é a primeira vez que o OMNIRA ganha qualquer notificação in-app, e o caso mais
imediato e já pronto do lado de trás é o SLA de conversas 1:1 (ADR-0044), que já dispara eventos sem ninguém
ouvindo. Esta ADR cobre só essa fatia; alerta de grupo (mensagem real enviada a um grupo de confiança da
supervisão) fica para uma fase 2, com ADR própria (ver "Questões em aberto").

## Contexto
`internal/routing/engine/sla.go` (ADR-0044 fase 5c) já calcula risco e estouro de SLA por conversa (métricas:
primeira resposta, espera em fila, atribuição, resolução) e já grava `job.routing.sla_at_risk.v1` /
`job.routing.sla_breached.v1` no outbox durável. Nada consome esses eventos hoje — nenhuma tela, nenhum alerta.

Separadamente, o OMNIRA já tem um pipeline de tempo real pronto e em uso (P1b, migração 000026): triggers de
`messages`/`conversations` chamam a função SQL `realtime_emit(tenant, type, conversation, data)`, que faz
`pg_notify` num canal que o worker (`internal/worker/realtime/bridge.go`) escuta e republica no NATS, em
`inbox.events.<tenant>.<conversation>` (um stream por conversa) ou `inbox.events.<tenant>.>` (stream do tenant
inteiro, usado pelas telas de Inbox). O frontend já tem `useRealtimeEvents` consumindo isso e um sistema de toast
global já montado (`ToastRegion`/`showToast`, no `Layout`) que já é "aparece e some sozinho" — exceto erro, que
fica até a pessoa fechar.

## Decisão
1. **Reaproveita o pipeline de tempo real que já existe, sem nenhum componente novo de transporte.** Nenhuma
   tabela nova, nenhum endpoint novo, nenhum WebSocket/SSE novo. `slaEvent()` (onde o outbox event já é gravado)
   passa a também chamar `realtime_emit` **na mesma transação**, com `type` = `"sla_at_risk"` ou `"sla_breach"` e
   `data` = `{"metric": ...}` — o mesmo payload mínimo (nunca conteúdo de mensagem) que os triggers de mensagem já
   seguem. É um efeito colateral a mais na função que já existe, não um processo novo.
2. **Broadcast a todo o tenant, sem filtro por papel ou setor.** O stream tenant-wide (`inbox.events.<tenant>.>`)
   já não filtra por quem conecta além de autorização de tenant; "todo mundo vê, inclusive o agente" (decisão
   explícita do dono do produto) é o comportamento natural de não adicionar filtro nenhum — mais simples que
   decisão anterior (só supervisores), que foi corrigida em seguida.
3. **Só aparece em tela de Inbox/Conversa, por ora.** A conexão tenant-wide hoje só é aberta pelas telas de Inbox
   (`InboxPage`, `InboxWorkspace`, `ChatPane`, `ConversationPage`). Fatia pequena: o handler do novo tipo de
   evento entra nelas, chamando `showToast()`. Quem está em Grupos/Tickets/Dashboard no momento do evento não vê
   o toast — aceito conscientemente (ver "Alternativas"), não é um bug, é o tamanho da fatia 1.
4. **Toast, nunca persistência.** Decisão explícita: sem tabela de notificações, sem sininho, sem histórico. Quem
   não estava com uma tela de Inbox aberta no instante do evento simplesmente não vê — mesma régua de "realtime é
   best-effort e efêmero" que já vale pro resto do pipeline (comentário da migração 000026).
5. **Severidade usa o vocabulário de toast que já existe.** `sla_at_risk` → `kind: "info"` (some sozinho,
   ~5s); `sla_breach` → `kind: "error"` (fica até a pessoa fechar, porque é mais urgente e a pessoa precisa de
   tempo pra ler e agir — mesma regra que já vale pro resto dos toasts do app). Nenhum tipo novo de toast.
6. **Texto do toast nunca carrega conteúdo de conversa.** Só a métrica e, no máximo, um identificador curto —
   mesma régua de privacidade que a migração 000026 já aplica a `message_received`/`conversation_updated`.

## Consequências
- Ganho: primeiro alerta de verdade do OMNIRA, usando um evento de SLA que já existia e já era pago (CPU, gravação
  no outbox) mas não tinha consumidor nenhum — ganho real sem custo de infraestrutura novo.
- Custo: quase nulo — uma chamada SQL a mais na função que já existe, um `case` a mais nos handlers de evento que
  já existem nas telas de Inbox.
- **Tenancy:** nenhuma mudança de superfície — o stream tenant-wide já é autorizado por tenant; a bridge já
  verifica que a conversa pertence ao tenant antes de publicar (mesma defesa que já existe para
  `message_received`).
- **Reversibilidade:** reverter é remover a chamada a `realtime_emit` em `slaEvent()` e o `case` novo no frontend;
  nenhuma migration, nenhum dado armazenado a desfazer.
- **Limitação aceita:** só visível em tela de Inbox/Conversa; ver "Alternativas" pro que ficaria diferente com o
  escopo maior.

## Alternativas
- **Mover a conexão tenant-wide pro nível do `Layout`** (uma única conexão compartilhada por toda a aplicação, em
  vez de cada tela de Inbox abrir a sua): alerta visível em qualquer tela do sistema. Rejeitada por agora —
  decisão explícita do dono do produto de manter a fatia pequena; mexe em código de Inbox que já funciona bem.
  Fica como possível fase 1.5 se a limitação incomodar na prática.
- **Nova tabela de notificações + sininho:** mais robusto (sobrevive a quem não estava online), mas escopo bem
  maior (UI de lista, estado lido/não lido). Rejeitada por agora — decisão explícita de ficar só com o toast
  passageiro, fiel ao "aparece e depois some" pedido originalmente.
- **Alerta só para supervisores do setor:** considerada primeiro (usando `sectors.supervisor_user_id`), corrigida
  pelo próprio dono do produto para broadcast a todo o tenant antes da implementação começar.

## Questões em aberto
- **Fase 2 (fora desta ADR): alerta real enviado a um grupo de WhatsApp.** O dono do produto quer, eventualmente,
  que alertas de escalonamento cheguem como mensagem de verdade a UM grupo específico, pré-eleito e de confiança
  da supervisão — nunca um grupo de cliente, nunca qualquer grupo. Isso quebra a regra de "nada é enviado a grupo"
  que vem desde a ADR-0015/0055/0056, então precisa de ADR própria que trate essa exceção com cuidado (allowlist
  de exatamente um grupo por tenant, nunca liberado por engano para um grupo de cliente). "Só se for viável ou
  fizer sentido" — não é um compromisso, é uma possibilidade a avaliar quando a fase 1 estiver provada.
- **Alerta de escalonamento de GRUPO** (categoria H/N do mapeamento de tratativas,
  `docs/research/group-tratativas-ia/mapeamento-v1.md`) ainda não alimenta este pipeline — hoje só SLA de
  conversas 1:1 dispara. Juntar os dois é uma decisão futura, não desta ADR.
- Se a limitação "só aparece em tela de Inbox" incomodar na prática, reabrir a alternativa do `Layout` acima.
