# ADR-0055: IA observadora de mensagens de grupo (classificação sugerida, nunca aplicada)

## Status
Aceito e implementado (2026-11-09), a pedido do dono do produto: "vamos preparar isto... já indo em direção da
proposta do agente pra testar e ensinar a compreender o que o cliente solicita", com o modo escolhido explicitamente
entre as opções apresentadas: **"Só observar e sugerir, sem agir"** — nada visível ao cliente, nenhum envio.

Cumpre a reserva do item 7 de ADR-0015: *"Automações de grupo são outro assunto... terão ADR e fila de trabalho
próprios e não reutilizam roteamento, atribuição nem as automações da conversa individual."* Este ADR é essa fila
própria.

## Contexto
ADR-0015 já lê e guarda mensagens de grupo (`wa_groups`/`wa_group_messages`), somente leitura, opt-in por grupo. O
dono do produto quer começar a ensinar uma IA a entender o que um cliente está pedindo dentro de um grupo —
primeiro passo de uma proposta maior ("Hermes Agent"), mas **sem nenhuma ação automática ainda**: só observar e
sugerir, para validar a classificação antes de cogitar qualquer automação.

Já existe, para conversas 1:1, um pipeline maduro de IA (`internal/intelligence`, ADR-0017): evento -> job durável
(`intelligence_jobs`) -> `JobRunner` -> `TopicClassifier.ClassifyShadow` (grava uma proposta em `routing_decisions`
com `applied=false`, nunca aplicada sem o flag `OMNIRA_TOPIC_AUTO_ROUTING_ENABLED`). A tabela `intelligence_jobs` e o
gatilho que a alimenta até aceitam `group_message_id` (preparado desde a ADR-0015/Wave de ADR-0017), mas os flags
que ligam essa IA (`OMNIRA_TOPIC_AI_ROUTING_ENABLED`) são **globais, por variável de ambiente, para a plataforma
inteira** — ligá-los para testar num único grupo também ligaria a classificação por IA das conversas 1:1 de **todo
cliente pagante**, sem decisão própria disso e fora do que foi pedido. Por isso esta ADR não reusa esse pipeline;
abre o ponto de extensão que a própria ADR-0015 deixou reservado.

## Decisão
1. **Opt-in por grupo, independente de tudo o mais.** Nova coluna `wa_groups.ai_observer_enabled` (padrão
   `false`), `ai_observer_set_by`, `ai_observer_set_at`. Independente de `enabled` (leitura) e `purpose`; um grupo
   pode ler mensagens sem ter o observador de IA, e vice-versa não é permitido (sem leitura não há mensagem para
   observar). Alterado pelo mesmo `PATCH /groups/{id}` já usado para `enabled`/`purpose`, exigindo `group.manage`.
2. **Interruptor geral, separado do por-grupo.** Variável de ambiente `OMNIRA_GROUP_AI_OBSERVER_ENABLED` (padrão
   `false`) liga ou desliga o processo todo no worker — um kill switch de operação, no mesmo padrão dos outros
   flags do projeto (`OMNIRA_TOPIC_THREADS_ENABLED`, `OMNIRA_MULTIMODAL_ANALYSIS_ENABLED`). Com o flag geral ligado e
   nenhum grupo com `ai_observer_enabled=true`, nada muda e nenhuma chamada de IA acontece — o custo e o risco só
   existem para quem decidiu entrar.
3. **Fila própria, não a de ADR-0017.** Um novo processo leve de varredura (`internal/groups/application`), no
   mesmo estilo já usado pelo processador de mídia (`internal/media/application/processor.go`): roda em sessão de
   sistema (`is_system_admin()`), reivindica com `FOR UPDATE SKIP LOCKED` as mensagens de grupos com
   `enabled=true AND ai_observer_enabled=true` que ainda não têm sugestão, pula tenants suspensos. Nunca toca
   `intelligence_jobs`, `routing_decisions` nem o `JobRunner` de ADR-0017.
4. **Só mensagem de participante, só texto, por ora.** Mensagens da própria linha (`from_me=true`) e mensagens sem
   corpo (mídia pura) são ignoradas nesta fase — mesma régua que ADR-0015 já aplicou ("mídia fica fora das
   primeiras fatias").
5. **Nova tabela, só a sugestão, nunca a ação.** `wa_group_message_suggestions`: uma linha por mensagem
   (`UNIQUE(tenant_id, group_message_id)`, idempotente), com `category` de uma lista fechada (`request`,
   `complaint`, `question`, `info`, `other` — rotulada em pt-BR na interface), `summary` (texto curto, limite de
   caracteres), `confidence`, `model_provider`, `model_name`, `latency_ms`, `created_at`. RLS forçada: leitura por
   quem já tem `group.read` (admin/supervisor); escrita só por sessão de sistema. Nenhuma automação lê essa tabela
   para agir — hoje ela só é servida para exibição.
6. **Prompt fixo, mensagem é dado.** O gerador de texto (`internal/ai/ports.TextGenerator`, o mesmo usado por
   `internal/ai/application/summarize.go`) recebe instruções fixas do servidor pedindo UMA categoria da lista
   fechada e um resumo de uma linha; o corpo da mensagem é sempre dado de entrada, nunca instrução — mesma defesa
   contra injeção de prompt que o resumo de conversa já usa.
7. **Nada visível ao cliente, nada enviado, nada na API externa.** A sugestão nunca é escrita de volta no grupo,
   nunca aciona `SendText`, nunca aparece em `internal/external` (a API do CRM, ADR-0051 já não leva nada de IA para
   lá) — só na aba Grupos do Workspace, para quem tem `group.read`.
8. **Primeiro grupo: o de teste da própria K3G.** O dono do produto liga o observador manualmente só no grupo de
   homologação interno, vinculado à própria conta K3G (ADR anterior desta sessão), antes de cogitar oferecer isso a
   um grupo de cliente de verdade.
9. **Observabilidade.** Contadores por resultado (classificado, pulado, falhou), nunca o corpo da mensagem nem a
   resposta do modelo nos logs.

## Consequências
- Ganho: primeiro passo real e barato em direção a "ensinar a IA a entender o que o cliente pede" em grupos, sem
  arriscar o pipeline de IA de conversas 1:1 nem gerar custo/risco para quem não optou.
- Custo: mais uma tabela, mais um processo no worker, mais uma tela. Pago de propósito para não reabrir ADR-0017.
- **Tenancy:** toda consulta exige `TenantContext`/RLS; testes obrigatórios Tenant A/B (grupo de um tenant nunca
  gera nem mostra sugestão para outro), membership revogada perde a leitura das sugestões imediatamente.
- **Reversibilidade:** migrations com `down`; desligar o flag geral para o processo, ou o opt-in de um grupo,
  interrompe novas sugestões; as já geradas somem só quando o histórico do grupo é apagado (ADR-0015 G6).

## Alternativas
- **Ligar `OMNIRA_TOPIC_AI_ROUTING_ENABLED` e deixar o `ShadowPipeline` de ADR-0017 processar também os grupos** (a
  tabela já aceita `group_message_id`): mais rápido de construir, mas o flag é global — ligaria a classificação por
  IA das conversas 1:1 de todo cliente pagante só para testar num grupo interno. Rejeitada; é exatamente o que
  ADR-0015 §7 já previu e reservou para ADR própria.
- **Aplicar a sugestão automaticamente (marcar o grupo, abrir chamado, etc.):** fora do modo escolhido ("só
  observar e sugerir, sem agir"). Fica para quando houver decisão própria sobre automação de grupo.

## Questões em aberto
- Quando (e se) uma sugestão vira automação (alerta, chamado, resposta) é decisão futura, com ADR próprio — esta
  ADR não abre esse caminho.
- Se/quando outro grupo de cliente real pedir o mesmo recurso, cabe ao dono do produto decidir ligar o opt-in para
  ele; nada aqui liga automaticamente para um grupo novo.
