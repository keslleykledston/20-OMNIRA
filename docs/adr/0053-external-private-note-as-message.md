# ADR-0053: Nota interna na API externa como mensagem `private` (Opção B — paridade com Chatwoot)

## Status
**Aceito e implementado (2026-10-20).** Reabre G3 de `docs/ops/HEADLESS-CHATWOOT-MAPPING.md` §4 ("Não expor agora... Alto para o ganho; recomendo adiar"), a pedido explícito do dono do produto nesta sessão, escolhendo a opção de maior fidelidade ao Chatwoot (e maior risco) em vez da alternativa mais simples.

**Revisão independente (Codex CLI, `codex review --uncommitted`)**, exigida pela seção "Revisão" abaixo, encontrou dois problemas reais que a implementação original não previa — nenhum deles no caminho externo em si, e sim nos outros consumidores da mesma tabela `messages`:
1. O cálculo de "cliente esperando resposta" (Workspace e projeção do Hub) tratava uma nota privada como se fosse uma resposta real, zerando o indicador de espera sem o cliente ter sido respondido de fato.
2. O feed de mensagens do Workspace e do Hub, a busca de histórico do contato e o resumo do Contact 360 mostravam a nota privada como se fosse uma mensagem normal, sem o rótulo `private`, arriscando um operador humano achar que aquele texto foi enviado ao cliente.

Ambos corrigidos antes de aceitar a fatia como pronta: `internal/inbox/adapters/http.go`, `internal/worker/hubprojector/projector.go`, `internal/hub/adapters/http.go`, `internal/attendance/adapters/postgres.go` e `internal/contacts/adapters/contact360.go` agora excluem `private` de toda contagem de "não respondido"/"última mensagem" e de todo feed fora da API externa — uma nota continua inteiramente invisível para qualquer coisa que não seja a própria API externa, até que uma extensão de UI própria do Workspace decida como exibi-la rotulada. Detalhes e testes no CHANGELOG.

## Contexto
Pendência §3.4 de `OMNIRA_API_PENDENCIAS_CRM.md`: no Chatwoot, uma mensagem com `private: true` é uma **nota interna** — nunca enviada ao cliente, mas que aparece **intercalada no mesmo feed de mensagens**, com autor e horário, igual a qualquer outra. O CRM quer essa paridade; hoje grava a nota só no próprio ticket.

Hoje o OMNIRA tem `contact_notes` (migration `000079`): nota de texto **do contato**, não da conversa, escrita só por `tenant_user` do Workspace, sem nenhuma ligação com `messages`. Não é a mesma coisa — não aparece no feed, não tem ligação a uma conversa específica, não é alcançável pela API externa.

A API externa não tem nenhum conceito de mensagem privada: toda linha de `messages` que entra por `POST /conversations/{id}/messages` é candidata a entrega real (WAHA ou Meta Cloud API) conforme a janela de 24h e o canal.

## Decisão

### 1. Schema
`messages.private BOOLEAN NOT NULL DEFAULT false` (migration aditiva). Uma nota:
- é uma linha normal de `messages` (mesma tabela, mesma ordenação por `created_at`/`id`, mesma paginação de `GET .../messages`);
- `direction = 'outbound'`, `sender.kind = 'crm_application'` (ou `'agent'`/`'automation'` se escrita pelo Workspace/fluxo — fora de escopo nesta fatia, ver "Fora de escopo");
- nunca tem `media` (texto simples nesta primeira fatia, igual ao que o Chatwoot também restringe para nota).

### 2. Barreira contra vazamento (o ponto mais importante deste ADR)
Uma nota privada **nunca** pode chegar ao WAHA ou à Meta Cloud API. Isso não é uma regra de UI — é um invariante de dado reforçado em **todas** as camadas, nenhuma delas opcional:
1. **Escrita**: `private=true` é decidido no comando de escrita e gravado na mesma transação que cria a mensagem; a chamada que hoje enfileira a entrega (`internal/messages/application/external_send.go` e o caminho equivalente do Workspace, se vier a existir) checa `private` **antes** de qualquer outra coisa e, se verdadeiro, **nunca** chama o `Sender`/fila de entrega — a mensagem nasce com um status final (ex.: `status='delivered'` com um marcador de "não enviado por ser nota", a definir na implementação) sem passar pelo pipeline do provedor.
2. **Teste de invariante, não só de comportamento**: além do teste funcional ("a nota aparece no feed"), um teste prova que, criando N notas privadas em sequência (inclusive concorrente), **zero** chamadas chegam ao cliente mock do WAHA/Meta e **zero** linhas aparecem na fila/; job de entrega. Isso é condição de aceite, não "nice to have".
3. **Revisão independente**: a lógica de barreira (item 1) passa por revisão externa antes de aceitar a fatia como pronta (ver "Revisão" abaixo) — especificamente procurando por um caminho (atual ou futuro) que ignore o `private` e mande a nota ao provedor.

### 3. Endpoint e scope
Endpoint **separado** de `POST /conversations/{id}/messages`, não uma flag no mesmo payload: `POST /conversations/{id}/notes`, corpo `{ text }` (até 4000 caracteres, mesmo limite de `contact_notes`), com `Idempotency-Key` e `If-Match` no mesmo padrão dos outros comandos de escrita (`write_http.go`). Caminho de escrita separado reduz o risco de um campo esquecido (`private: false` por omissão) transformar uma nota em mensagem real para o cliente — o tipo de defeito que esta ADR existe para evitar.

Novo scope `message.note`, independente de `message.send` (uma aplicação pode ter um sem o outro — mínimo privilégio, mesmo padrão dos demais scopes do catálogo fechado em `internal/external/domain/scope.go`).

### 4. Leitura
`GET /conversations/{id}/messages` passa a incluir notas intercaladas, com `private: true` no corpo. Visível a qualquer instalação que já tenha `message.read` nessa conversa (mesma visibilidade de sempre) — ver "Questões em aberto" sobre restringir a quem tem `message.note`.

### 5. Eventos/webhook
`message.created.v1` dispara igual para nota (o evento já é só referência — `message_id`, nunca o corpo — então não há vazamento adicional pelo webhook).

### 6. Tenancy e testes obrigatórios
Tenant A/B, tentativa de acesso cruzado, leitura e escrita, credencial revogada, tudo contra PostgreSQL real — mesmo padrão de todo o resto da API externa.

## Fora de escopo nesta fatia
- Nota escrita pelo Workspace (pessoa humana) na conversa — isso seria uma extensão de UI própria, não pedida pelo CRM; `contact_notes` continua sendo o mecanismo do Workspace até/-se houver pedido.
- Editar ou apagar uma nota depois de criada (o Chatwoot permite; o CRM não pediu isso explicitamente — se precisar, é extensão simples do mesmo endpoint).
- Nota em mensagem de grupo (ADR-0051, não implementado).

## Consequências
- Ganho: paridade real com o que o CRM tinha no Chatwoot — a nota aparece na mesma timeline, sem o CRM ter que combinar duas fontes.
- Custo/risco: todo código futuro que tocar o pipeline de envio (`internal/messages/application`, o worker de entrega, qualquer novo canal) herda a obrigação de nunca ignorar `private`. Isso é uma dívida de atenção permanente — exatamente o "alto custo para o ganho" já registrado na decisão original de adiar.
- Migration nova em `messages` (tabela quente, todo o tráfego passa por ela) — aditiva e reversível (`down` remove a coluna; notas existentes seriam perdidas no rollback, aceitável por serem informação só interna).

## Alternativas
- **Opção A (rejeitada pelo dono nesta sessão):** nota ligada à conversa via `contact_notes` + `conversation_id`, fora do feed de mensagens. Mais simples e sem risco de vazamento ao provedor (não toca `messages` nem o pipeline de envio), mas não dá a experiência de feed único que o CRM pediu.
- **Marcar `status` em vez de coluna `private`:** usar um valor de `status` (ex. `'internal'`) em vez de um booleano dedicado. Rejeitada: `status` já documenta o ciclo de entrega real (`received/queued/sent/delivered/read/failed/uncertain`); misturar "é nota" com "estado de entrega" no mesmo campo cria combinações sem sentido (`status='internal', direction='inbound'`?) e dificulta a barreira do item 2 (teria que checar uma string em vez de um booleano único).

## Revisão
Antes de aceitar a implementação desta fatia como pronta: revisão independente da barreira contra vazamento (item 2) via `codex review --commit <sha>` (Codex CLI, autenticado nesta máquina), focada especificamente em "existe algum caminho, hoje ou introduzido por este diff, em que `private=true` ainda alcança o Sender/WAHA/Meta?". Resultado anexado ao PR.

## Questões em aberto
1. Leitura restrita a quem tem `message.note`, ou qualquer instalação com `message.read` vê as notas de outra aplicação? (Proposta acima: qualquer uma com `message.read`, por simetria com o resto do feed — mas é decisão de produto, não técnica.)
2. Nome final do status "entregue mas é nota" (`status='delivered'` com marcador, ou um valor novo de `status`?) — detalhe de implementação, decidir na fatia.
