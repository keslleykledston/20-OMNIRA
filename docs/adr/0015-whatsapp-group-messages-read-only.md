# ADR-0015: Mensagens de grupo do WhatsApp (somente leitura, em área própria)

## Status
Accepted em 2026-10-04 por instrução do dono do produto ("construa a ADR-0015"). Fatias G1 a G4 (backend) implementadas; G5 (aba Grupos) e G6 (retenção quente/fria) implementados. Ver "Implementação" ao final.

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

## Implementação (2026-10-04)

- **G1 - modelo e permissões:** migrations `000057` (`wa_groups`, `wa_group_messages`, RLS forçada com as quatro
  políticas, `GRANT` explícito, índice de paginação) e `000058` (`group.read`, `group.manage` para admin e supervisor).
- **G2 - habilitação:** `GET /groups/available` (grupos da conta WhatsApp do tenant, com busca por nome e teto;
  o WAHA/GOWS devolve ~600 grupos com todos os participantes, então a lista é filtrada e guardada em cache por 1 min),
  `POST /groups` (o grupo precisa existir na conta do próprio tenant), `PATCH /groups/{id}` (liga/desliga) e
  `DELETE /groups/{id}/messages` (apaga o histórico). Tudo auditado (`group.enabled`, `group.disabled`,
  `group.history_deleted`).
- **G3 - ingestão:** o webhook ganhou ramo próprio para `@g.us` (autor = `participant`, nome = `PushName`). Grupo
  não habilitado é descartado **antes de qualquer gravação**, inclusive do registro de deduplicação, e responde 202;
  um grupo nunca vira 400 (evita a tempestade de retentativas que originou o descarte). Idempotente por evento e por
  `provider_message_id`; `last_message_at` nunca retrocede com entrega fora de ordem. Sem ingestor configurado o
  comportamento anterior (202) se mantém.
- **G4 - leitura:** `GET /groups` (habilitados, com prévia da última mensagem) e `GET /groups/{id}/messages` (cursor,
  mais nova primeiro). O histórico de um grupo desligado continua legível até ser apagado.
- **Decisões desta implementação:** mensagens da própria linha (do celular conectado) no grupo também são lidas,
  rotuladas "Você"; o autor é um endereço opaco do WhatsApp (muitas vezes `@lid`), **nenhum telefone é guardado nem
  servido**; o corpo é limitado a 8000 caracteres; mídia entra apenas como marcador tipado (sem baixar o arquivo);
  não há envio; a interface atualiza por consulta periódica (um evento em tempo real por mensagem de grupo faria o
  Inbox inteiro recarregar a cada mensagem de um grupo ruidoso).
- **Testes** (Postgres real, Tenants A e B): ingestão só de grupo habilitado (com mutação), duplicata, ordem,
  isolamento, permissões por papel, membership revogada, sem oráculo de enumeração entre tenants, cursor sem lacunas,
  desligar/apagar; parser e handler do webhook; cliente WAHA; varredura de RLS das tabelas novas.

### Questões em aberto - situação
- Quais grupos entram primeiro: **nenhum por padrão**; o administrador escolhe na aba (a sugestão continua sendo só
  os de trabalho).
- Retenção padrão e expurgo automático (G6): **pendente do dono**; hoje só há "apagar histórico" manual.
- Quem lê: administrador e supervisor (`group.read`); atendente não.
- Mídia: marcador tipado agora; baixar o arquivo fica para depois.
- Notificações e envio ao grupo: fora desta fase.

### G6 - retenção quente/fria (2026-10-04, padrões aprovados pelo dono)
- **Quente:** 30 dias no Postgres (o que a aba Grupos lê). **Frio:** o que passa disso vai, por `scripts/archive-wa-groups.sh`
  (cron diário, 07:30 UTC = 03:30 de Manaus), para arquivos `.ndjson.gz` no disco externo
  (`<disco>/Backup/omnira_groups/<tenant>/<grupo>/<AAAA-MM>/<lote>.ndjson.gz`), por **12 meses**; depois os arquivos são
  removidos (o registro do lote fica, como `expired`).
- **Segurança contra falha do USB:** o banco só apaga **depois** que a cópia foi conferida (tamanho e sha256). Cada exportação é um
  *lote* marcado no banco antes (`wa_group_archive_batches`, `wa_group_messages.archive_batch_id`); cópia com nome temporário e
  renomeação atômica; apagamento e marcação "concluído" na mesma transação, com a contagem conferida (se divergir, desfaz e
  reexporta). Disco ausente, cópia que falha, truncada ou corrompida, ou linhas que mudam durante a cópia: nada é apagado, o lote
  fica pendente e a próxima rodada retoma sem duplicar. Duas rodadas simultâneas: a segunda cede (`flock`).
- **Disco local protegido:** teto de 1 GiB nas tabelas de grupos (`OMNIRA_GROUPS_MAX_BYTES`, 0 desliga). Acima dele só a gravação
  de grupos pausa (contador `group_message_total{outcome="paused_size_limit"}` e log); nada já guardado é apagado e as conversas
  1:1 não são afetadas. O tamanho é lido no máximo uma vez por minuto.
- **Alerta:** `scripts/archive-wa-groups-check.sh` (mesmo contrato dos outros checks, via `run-check-with-alert.sh`): WARN após 26 h
  sem sucesso, FAIL após 48 h; o marcador só avança quando a rodada termina sem pendência.
- **"Apagar histórico"** também pede (`wa_groups.archive_purge_requested_at`) a remoção dos arquivos frios do grupo, feita pelo job
  (só ele alcança o disco externo). Os dumps de backup já feitos continuam contendo as mensagens até expirar; por isso os dumps no
  disco externo passaram a ter retenção de **35 dias** (antes: sem prazo), um pouco mais que a nuvem (30). A tela diz isso.
- Testes: `scripts/test-archive-wa-groups.sh` (56 verificações em Postgres descartável, com falhas do USB injetadas e mutação da
  conferência da cópia), `scripts/test-archive-ops.sh` (verificador e poda dos dumps) e os testes em Go do teto e do pedido de limpeza.

## Addendum: identificar quem fala num grupo (nome aparente, empresa, função) — sem tocar `contacts`

Pedido do dono (2026-11-12): cadastrar quem participa dos grupos — nome aparente, empresa e função — para
que a mesma pessoa (ex.: "Felipe, Suporte K3G") seja reconhecida em todos os grupos onde aparece.

O plano inicial propunha resolver `author_jid` para um `contacts.phone_e164` (reaproveitando
`contacts`/`contact_account_links`, já usados pela conversa 1:1). Isso foi **rejeitado**: contraria direto a
tabela da Decisão acima (`contacts.phone_e164 obrigatório e único` × "Grupo não tem telefone") e, na prática,
`author_jid` **é frequentemente um `@lid`** (o WhatsApp mascara o remetente num grupo por privacidade) — não
há telefone real pra normalizar na maioria dos casos. Forçar o fluxo de `contacts` teria falhado silenciosamente
pra boa parte dos participantes, exatamente os que se queria identificar.

**Decisão:** o perfil (nome aparente, empresa, função) fica direto em `channel_participants` — a entidade que
já identifica "quem escreveu" por `(tenant_id, channel_connection_id, provider, external_participant_id)`,
sem exigir telefone e já estável entre grupos da mesma conexão (é exatamente o "em todos os Grupos" pedido).
Nenhum telefone passa a ser guardado; `contacts`/`conversations`/`messages` continuam intocados, como a
Decisão original já mandava.

- `channel_participants` ganha `alias` (nome aparente, mesma semântica do `contacts.alias`: vazio = usa o
  nome do WhatsApp), `company_account_id` (FK para `customer_accounts`, a "empresa" — uma empresa interna
  do próprio tenant usa `account_type='internal'`, sem conceito novo) e `company_role` (mesmo vocabulário
  fechado de `contact_account_links.relationship_type`, pelas mesmas razões de consistência). `company_role`
  exige `company_account_id` (constraint).
- O "papel" que a empresa exerce num grupo específico (Consultoria/Agente-Cliente/etc.) **não** é um campo
  novo: já é dado pela classificação do próprio grupo (`wa_groups.purpose`/`account_id`, ADR-0059). O perfil
  do participante é global ao tenant; o grupo já diz em que papel aquela empresa está ali.
- Exposto só dentro da área de Grupos (`internal/groups/adapters/http.go`): listar quem já escreveu num
  grupo, editar o alias e ligar/desligar a empresa. `group.manage` para editar, `group.read` para ver —
  mesmas permissões já usadas no resto desta ADR. O `external_participant_id` (o JID/telefone cru) nunca é
  servido por essas rotas, mesma regra de "o número só aparece onde for estritamente necessário".
- **Menção (`@<número>`) resolvida pelo mesmo motivo.** Quando o remetente digita uma menção, o WhatsApp
  guarda isso no corpo da mensagem como `@<dígitos>` — o mesmo número/`@lid` cru que esta ADR já evita
  mostrar. `ListMessages` troca `@<dígitos>` por `@<nome>` sempre que os dígitos batem com um
  `channel_participants` já conhecido NA MESMA conexão (nunca telefone, nunca busca externa); um número
  desconhecido fica exatamente como foi digitado. `internal/groups/adapters/http.go`'s
  `participantMentionNames`/`resolveMentions`.
- **Retenção da imagem segue a do `internal/media` (60 dias), não a do texto (30 quente/12 meses frio,
  Retenção acima).** `wa_group_message_media` é processada pelo MESMO `mediaapp.Processor` de 1:1 — inclusive
  o expurgo automático (`ExpiredFiles`/`MarkPurged`, embutido no próprio `Processor.Run`) — sem nenhum job
  novo. É uma inconsistência deliberada (reaproveitar o pipeline inteiro, inclusive seu prazo, em vez de
  construir um expurgo próprio só para alinhar um número); revisar se isso incomodar na prática.

## Addendum 3 — Cliente do Cliente (migration 000201)

**Problema:** a empresa vinculada a um participante (addendum anterior) nem sempre é a própria empresa do
Cliente — pode ser cliente DELA. Exemplo real: um contato escreve num grupo da 4WNET (Cliente do tenant);
a empresa dele é a Uplink, que é cliente da 4WNET, não funcionária/representante dela. `company_role`
(Funcionário/Proprietário/Contato técnico/.../Prestador) não cobre esse caso — nenhuma dessas opções diz
"esta empresa é cliente do Cliente".

**Decisão:** `channel_participants` ganha `is_downstream_client BOOLEAN NOT NULL DEFAULT FALSE` — um
checkbox simples ("Cliente") ao lado do campo Empresa no modal "Identificar participante", marcado junto
com a empresa vinculada. Exige `company_account_id` (constraint, mesmo padrão de `company_role`). Não reusa
`customer_accounts.account_type` (`customer`/`partner`/`internal`/`other`): aquele campo classifica a conta
em relação ao TENANT (K3G), não em relação ao Cliente que o grupo já representa — a Uplink não precisa ser
cadastrada como "cliente direto" do tenant para a K3G, ela é cliente da 4WNET, então marcar sua
`customer_accounts` como `customer` classificaria errado a relação com o próprio tenant. Um booleano
contextual no participante evita inventar uma hierarquia nova entre contas.

- Exposto em `GroupParticipant.is_downstream_client` e `GroupMessage.is_downstream_client` (nulo quando não
  há empresa vinculada). Editado via o mesmo `PUT .../participants/{id}/company/link` que já seta
  `account_id`/`role`; `DELETE .../company/link` zera também este campo.
- UI: badge "· Cliente" ao lado do nome da empresa, tanto no chat (`GroupThread.tsx`) quanto no modal de
  edição (`ParticipantProfileModal.tsx`).

