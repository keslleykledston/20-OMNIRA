# Handoff — gestão de filas e separação por setores (2026-10-12)

Levantamento feito no código de `main` (`6d083a5`), no banco de produção (somente leitura) e nos ADRs/docs. Nada foi alterado
no sistema por este levantamento. Onde não verifiquei algo, está dito.

## 1. Como as filas funcionam hoje

**Modelo (por empresa/tenant, com RLS):**
- `queues`: `name` (1 a 60, único por tenant), `mode` (`manual` | `round_robin`), `is_default` (no máximo uma por tenant).
- `queue_members`: pessoa na fila, com `active`, `available`, `capacity` (1 a 100) e `last_assigned_at`.
- `conversations.queue_id` aponta para a fila (FK `RESTRICT`: fila com conversas não é apagada).
- `tenants.routing_require_presence`: exige presença para o round-robin.

**Onde criar e gerenciar:** Configurações → **Pessoas e grupos** (`/settings/people`, aba `?aba=grupos`).
- Aba *Grupos* = filas: criar, renomear, modo, tornar padrão, excluir.
- Aba *Pessoas*: tornar operador, pausar, e **Gerenciar grupos** por pessoa (entrar na fila, disponível, capacidade).
- API: `GET/POST /tenants/{t}/queues`, `PATCH/DELETE /queues/{id}` (`agent.read` / `agent.manage`); os vínculos pessoa↔fila passam pelo perfil de agente (`agent_profiles_http.go`). Alterações são auditadas.
- **Armadilha de nome:** na interface "grupo" quer dizer fila. Já `/groups` e `group.read` são grupos de WhatsApp (ADR-0015). Os dois usos convivem.

**Roteamento de conversa nova:**
- `RouteNew` (`internal/inbox/adapters/postgres.go`) manda **sempre para a fila padrão** do tenant. Não existe regra por canal, por número, por assunto ou por cliente.
- Fila `round_robin`: o worker escolhe quem está ativo, disponível e abaixo da capacidade. Fila `manual`: a conversa espera alguém assumir.
- Não entram em fila: spam, contato interno (ADR-0018) e conversa com equipe verificada.
- **Assumir** exige ser membro ativo e disponível da fila da conversa, e estar abaixo da capacidade (`routing/adapters/assign.go`).

**Único mecanismo atual de "uma fila por setor":** o fluxo (Flow Builder). Os nós `assign_queue` e `human_handoff` mandam a conversa para uma fila escolhida, e o gatilho do fluxo pode ser restrito por linha de canal. Os packs (OMNIRA Starter, K3G Support, ISP NOC) pedem, na instalação, o mapeamento de filas "suporte, financeiro, comercial, NOC…".

## 2. Dados reais em produção (2026-10-12)

| Tenant | Fila | Modo | Padrão | Membros | Conversas |
|---|---|---|---|---|---|
| `11111111-…` (piloto) | Default | manual | sim | 1 | 77 |
| `11111111-…` (piloto) | Pilot Round Robin | round_robin | não | 2 | 5 |
| `4bae3cd1-…` | Default | manual | sim | **0** | 52 |

- Nenhuma fila de setor existe. Total de perfis de agente: 2. Work pools do Hub: 0.
- 5 fluxos publicados, todos no tenant piloto (Recepção inteligente, Chamado existente, Contexto da empresa, Transferir para humano, Contato desconhecido). Flag `OMNIRA_FLOWS_ENABLED=true`.
- O tenant `4bae…` tem 52 conversas numa fila sem nenhum membro. Pela regra de "assumir", ninguém consegue assumir essas conversas como agente. **Não verifiquei** se administradores contornam a regra; vale checar.

## 3. Existe planejamento para separar por setores?

**Resposta curta: só na intenção. Não há decisão, ADR, tabela nem tela de "setor".**

- `docs/product/PRD.md` §7.3 lista "filas por Tenant/departamento" e `docs/architecture/DATA-MODEL.md` cita `Department` como entidade. Nenhum ADR, migration ou tela implementa ou decide isso. Em `migrations/` não existe `department`.
- Na prática a **fila é o setor**: dá para criar quantas quiser por tenant (Comercial, Financeiro, Suporte, Servidores, Outsourcing TI, Monitoring) e vincular as pessoas pela tela acima. Isso funciona hoje, mas sozinho não separa o atendimento.
- O Hub (ADR-0038, migration 104) já presume várias filas por instância: um work pool pode responder por "uma fila de uma instância". Então a modelagem de fila-como-setor está alinhada com o Hub.
- Os packs de fluxo já pressupõem filas por assunto (suporte, financeiro, comercial, NOC).

### Lacunas para uma separação de verdade
1. **Entrada:** tudo cai na fila padrão. Para ir a um setor, a conversa precisa passar por um fluxo publicado. Faltam regra simples "esta linha/número → este setor" sem fluxo, e roteamento por assunto/empresa.
2. **Visibilidade:** a lista do Inbox mostra conversas de todas as filas a qualquer agente (devolve `queue_id`, mas não há filtro por fila nem restrição ao setor da pessoa). Só o *assumir* é limitado pela fila. Um agente do Financeiro lê as conversas do Comercial. O mesmo padrão da permissão `conversation.read_internal` (migration 120) serviria de modelo para um escopo por fila.
3. **Atributos do setor:** a fila não tem descrição, responsável/supervisor, horário, SLA, cor ou canal preferencial.
4. **Transferência entre setores:** não verifiquei se a tela do tenant permite mover uma conversa de uma fila para outra (o Hub tem `DelegatedTransferDialog`).
5. **Nomes:** "Grupos" na tela de filas colide com grupos de WhatsApp. "Setores" seria mais claro.
6. **Setores internos:** "Servidores", "Outsourcing TI" e "Monitoring" parecem atender empresas-clientes (contas/tickets) ou alertas automáticos. Não há definição de como esses setores recebem trabalho (por cliente? por tipo de chamado? por alerta?).

## 4. Proposta de fases (para decisão do dono; nada iniciado)

- **S0 — decisão e ADR:** fila = setor (recomendado, menor mudança) ou nova entidade `Department` que agrupa filas. Fixar o vocabulário na interface ("Setores").
- **S1 — operação sem código novo:** criar as filas dos setores no(s) tenant(s), vincular as pessoas e mapear os packs de fluxo para essas filas. Decidir a fila padrão como "Triagem".
- **S2 — regra de entrada:** linha de canal → setor, sem exigir fluxo (com a regra por assunto/empresa depois).
- **S3 — escopo de leitura por setor:** permissão/escopo para o agente ver só as filas das quais é membro; supervisores e administradores veem tudo. Mesma abordagem do ADR-0018 (decisão num ponto só, 404 fora do escopo, testes de tenant A/B).
- **S4 — atributos:** descrição, supervisor, horário, SLA e transferência entre setores na tela.

## 5. Decisões que dependem do dono
1. Fila é o setor, ou criamos `Department`?
2. O agente deve ver só as filas do setor dele (S3), ou todas, como hoje?
3. Quais tenants terão setores e quais são os setores de cada um? (os seis citados valem para todos?)
4. Como "Servidores", "Outsourcing TI" e "Monitoring" recebem trabalho?
5. Qual é a fila padrão do tenant `4bae…` (hoje sem membros) e do piloto?

## 6. Estado do que foi feito nesta sessão (para quem assume)
- **Atualizado em 2026-10-12 (noite):** `6d083a5` (permissão `conversation.read_internal`) e a migration 000120 estão em `main` **e implantados** (api, worker e web `sha-6d083a5`, construídos de um `git archive` de uma árvore limpa que tinha as duas linhas de trabalho; tags de volta `rollback-pre-final-20261012-1817`). `main` foi enviado ao GitHub (`f01fea8` na hora do envio). O texto original desta seção (antes da implantação) dizia "sem push" e "não implantado": já não vale.
- Armadilhas: checkout compartilhado muda de branch (construir de `git archive`/worktree limpo de `main`); `scripts/test-integration.sh` aceita só pacotes; `pgrep -f` em espera casa com o próprio comando.
- Pendências do dono: testar o fluxo "Interno" e a restrição (agente × supervisor), `#sair`, rotacionar segredos dos `.env.pre-*`, cobrança Meta 131042, fase de logo/favicon, e as cinco decisões da seção 5 (setores).
