# ADR-0015: Mensagens de grupo do WhatsApp (somente leitura, em área própria)

## Status
Proposed (aguardando aceite do dono do produto; nenhuma implementação autorizada)

Cumpre o item 4 do ADR-0014 ("Grupos ficam fora desta ADR"). Data: 2026-10-03.

## Contexto

Hoje mensagens de grupo do WhatsApp **não entram** no OMNIRA, por decisão de
projeto e não por falha:

- O webhook WAHA descarta `@g.us` e `status@broadcast` e responde 202 sem
  persistir nada (`internal/channels/adapters/waha/webhook.go`, ~linhas 188-202).
  O motivo registrado no código: o Inbox modela **um Contact e uma
  Conversation por remetente 1:1**, e o `from` de uma mensagem de grupo é o JID
  do grupo, não de uma pessoa. Tratá-lo como remetente falhava em
  `normalizeSender`, virava 400 e o WAHA reenviava o evento até 15 vezes.
- O volume é dominado por grupos: medido em 2026-09-21, **176 de 196** eventos
  `message.any` distintos em 40 minutos eram de grupo.
- O número conectado pelo piloto também é de uso pessoal e participa de muitos
  grupos de alto volume (vários com centenas de mensagens não lidas) ao lado de poucos
  grupos de trabalho da empresa (por exemplo, o grupo oficial interno e o de
  monitoramento). Ingerir tudo traria conversas privadas e ruído para uma
  ferramenta compartilhada.
- O produto pediu que grupos sejam **lidos**, mas em uma **aba diferente das
  conversas**, porque as tratativas e as automações de um grupo são diferentes
  das de uma conversa individual.

O que na conversa individual **não se aplica** a um grupo:

| Mecanismo da conversa 1:1 | Em grupo |
|---|---|
| Fila, roteamento, round robin, presença | Não há "quem atende" nem atribuição |
| Assumir / soltar / transferir | Sem dono único |
| "Aguardando" e tempo de espera (SLA) | Sempre haveria alguém "esperando"; sem sentido |
| Chamado (ticket) e vínculo com CRM | O grupo não é um cliente |
| Resposta idempotente por atendente | Nesta fase, nem há resposta |
| `contacts.phone_e164` obrigatório e único | Grupo não tem telefone |
| Uma pessoa por mensagem (o contato) | Autor diferente a cada mensagem |

Reaproveitar `contacts`/`conversations`/`messages` espalharia exceções
("exceto se for grupo") por roteamento, Dashboard, `RealTicketSQL`, exportação
CSV, Contact 360 e pela fila. O custo de errar uma dessas exceções é um grupo
entrando na fila de atendimento de clientes.

## Decisão

Proposta, em fatias pequenas, nesta ordem.

1. **Modelo próprio, separado de conversas.** Entidades novas, sem tocar
   `contacts`, `conversations` nem `messages`:
   - `wa_groups`: `id`, `tenant_id`, `channel_connection_id`, `provider_group_id`
     (o JID `...@g.us`), `name`, `enabled`, `enabled_by`, `enabled_at`,
     `created_at`, `updated_at`; único por `(tenant_id, channel_connection_id,
     provider_group_id)`.
   - `wa_group_messages`: `id`, `tenant_id`, `group_id`, `provider_message_id`
     (deduplicação, único por tenant + conexão), `author_jid`, `author_name`,
     `message_type`, `body`, `media_ref`, `mime_type`, `size_bytes`,
     `reply_to_provider_message_id` (opcional), `sent_at`, `created_at`.
   - Migrations forward-only (a próxima é a `000055`), com `ENABLE` e `FORCE ROW
     LEVEL SECURITY` e políticas de leitura, inserção e atualização por tenant,
     no mesmo padrão da `000052`. Índice de paginação por cursor em
     `(tenant_id, group_id, sent_at DESC, id DESC)`.
2. **Habilitação por grupo (opt-in), padrão desligado.** Um grupo só tem
   mensagens guardadas depois que um administrador o habilita. Grupo não
   habilitado continua sendo **descartado antes de qualquer persistência**,
   exatamente como hoje (202, sem retry). Isso preserva o comportamento atual
   para todo o resto e limita a coleta ao que a empresa decidiu.
   - A lista de grupos para o administrador escolher vem do WAHA da própria
     conexão do tenant (nunca de outro tenant).
   - Desabilitar interrompe a coleta; apagar o histórico é uma ação explícita e
     separada (ver Consequências, LGPD).
3. **Somente leitura nesta fase.** Sem composer, sem envio, sem reação. O
   envio a grupo exige decisão própria (a validação de destino de `SendText`
   hoje exige telefone E.164 e recusaria um `@g.us`) e fica para outro ADR.
4. **Autor por mensagem.** Cada mensagem guarda o participante que escreveu
   (`author_jid` normalizado e `author_name` sanitizado com a mesma regra que
   `sanitizeSenderName` já usa). A interface mostra o **nome**; o número só
   aparece onde for estritamente necessário.
5. **Permissões novas, mínimo privilégio.** `group.read` (ver a aba e as
   mensagens) e `group.manage` (habilitar/desabilitar, apagar histórico).
   Migration de permissões no padrão da `000043`: concedidas por padrão a
   `tenant_admin` e `tenant_supervisor`, **não** a `tenant_agent`. A autorização
   é avaliada dentro do `TenantContext`; `tenant_id` nunca vem do payload.
6. **Aba "Grupos" separada de "Conversas".** Item próprio no menu e rota própria
   (`/groups`), com lista de grupos habilitados (nome, última mensagem com
   autor, horário) e a conversa do grupo somente leitura. Mesmo padrão de leitura
   já adotado no Inbox: mais recente primeiro na lista, mensagens mais novas
   embaixo, paginação por cursor sem teto, busca por nome do grupo. **Sem**
   "Aguardando", sem atribuição, sem SLA, sem chamado. Segue
   `docs/architecture/FRONTEND-UX.md` e o gate visual do dono antes de entrar
   em produção.
7. **Automações de grupo são outro assunto.** Este ADR não define automações.
   Deixa apenas o ponto de extensão: um evento `group.message.received` (com
   `tenant_id`, `group_id`, `message_id`; nunca o corpo nem credenciais) no
   outbox/NATS, descrito no AsyncAPI. As regras de grupo (resumos, alertas,
   palavras-chave) terão ADR e fila de trabalho próprios e **não** reutilizam
   roteamento, atribuição nem as automações da conversa individual.
8. **Mídia.** Fica fora das primeiras fatias (texto primeiro). Quando entrar,
   usa o mesmo mecanismo seguro já existente (`MediaRetriever`: origem do WAHA
   confiável, tipo sniffado, conteúdo ativo bloqueado, tamanho limitado), sem
   caminho paralelo.
9. **Observabilidade.** Contadores por tenant de mensagens de grupo ingeridas,
   descartadas (grupo não habilitado) e rejeitadas (malformadas), mais latência
   do webhook. Os logs nunca registram corpo, JID de participante nem telefone.

## Plano em fatias

- **G1 — Modelo e permissões:** migrations de `wa_groups`/`wa_group_messages` com
  RLS forçado, permissões `group.read`/`group.manage`, contrato OpenAPI. Testes
  em Postgres real com Tenant A e B.
- **G2 — Habilitação:** listar grupos do WAHA da conexão, habilitar e
  desabilitar (auditado em `audit_events`).
- **G3 — Ingestão:** o webhook passa a persistir só grupo habilitado; idempotente
  por `provider_message_id`; descarte dos demais continua em 202.
- **G4 — Leitura:** API de lista e de conversa do grupo com cursor.
- **G5 — Interface:** aba "Grupos" (visual aprovado pelo dono antes do deploy).
- **G6 — Retenção:** prazo configurável e expurgo; ação "apagar histórico".
- **Depois, com ADR próprio:** envio ao grupo e automações de grupo.

## Consequências

- Ganho: grupos de trabalho passam a ser consultáveis sem contaminar fila,
  métricas, SLA, chamados nem Contact 360. A mudança de comportamento é
  restrita ao que o administrador habilitar; o restante do tráfego de grupo
  continua sendo descartado como hoje.
- Custo: modelo e interface novos (duas tabelas, duas permissões, uma aba). É a
  contrapartida de não abrir exceções no modelo 1:1.
- **Tenancy:** toda consulta em `wa_groups`/`wa_group_messages` exige
  `TenantContext`, RLS e `FORCE`. Testes obrigatórios: Tenant A e Tenant B,
  acesso cruzado em leitura e escrita, membership revogada, tentativa de ler
  grupo de outro tenant retornando o mesmo 404 de um id inexistente (sem oráculo
  de enumeração), e conexão de um tenant nunca listando grupos de outro.
- **LGPD e privacidade:** a coleta se limita a grupos habilitados por decisão
  administrativa; participantes são pessoas que não consentiram o tratamento
  individual, então a UI avisa sobre o uso, o telefone/JID do participante é
  minimizado, há prazo de retenção e "apagar histórico". A retenção padrão
  precisa ser definida pelo dono antes da G6.
- **Volume:** grupos habilitados podem gerar milhares de mensagens por dia.
  Índice por cursor desde o início, retenção como requisito (não opcional) e
  limite de tamanho do corpo guardado.
- **Webhook:** o desvio por grupo habilitado exige consultar `wa_groups` no
  caminho do webhook. Deve ser uma leitura barata (índice único) e sem alterar
  o contrato de resposta atual: 202 para ignorado, nunca 400 por grupo.
- **Contrato:** OpenAPI (rotas de grupos e permissões) e AsyncAPI
  (`group.message.received`) acompanham cada fatia.
- **Reversibilidade:** as migrations têm `down`; desligar a feature é
  desabilitar os grupos (volta ao descarte atual).

## Alternativas

- **`kind = 'group'` em `contacts` + conversa por grupo:** menor de implementar,
  mas obriga exceção em roteamento, fila, Dashboard, tickets e SLA, e `phone_e164`
  é obrigatório. Rejeitada; é a opção que mais chance dá de um grupo cair na
  fila de clientes.
- **Ingerir todos os grupos do número:** simples, porém traz volume dominante
  (cerca de 90% dos eventos) e conversas privadas de um número de uso pessoal.
  Rejeitada.
- **Não persistir e consultar o histórico no WAHA sob demanda:** evita guardar
  dados, mas sem histórico próprio, sem busca, sem automação e dependente do
  armazenamento do WAHA. Pode servir de fallback pontual, não como base.
- **Misturar grupos na aba Conversas com um filtro:** foi a primeira ideia e
  contradiz o pedido do produto, já que tratativas e automações diferem.
  Rejeitada.
- **Já permitir resposta no grupo:** amplia escopo e risco (mensagem pública
  em nome da empresa, sem fluxo de aprovação) sem decisão sobre quem pode
  escrever. Rejeitada para esta fase.

## Questões em aberto

- Quais grupos entram primeiro (a sugestão é só os de trabalho da empresa)?
- Qual a retenção padrão, em dias, e quem pode apagar o histórico?
- Quem lê: só administrador e supervisor, ou também atendentes?
- Mídia dos grupos entra na primeira entrega ou depois?
- Haverá notificação (por exemplo, menção ao operador ou alerta de palavra) ou
  só consulta? Isso define o escopo do ADR de automações de grupo.
- Quando o envio ao grupo for pedido, quem pode escrever em nome da empresa?
