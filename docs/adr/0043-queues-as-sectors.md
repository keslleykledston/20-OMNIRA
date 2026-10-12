# ADR-0043 — Setores: a fila é o setor; entrada por canal (S0–S2) e escopo de leitura (S3, proposto)

Status: **S0–S2 IMPLEMENTADAS e testadas (Postgres real + Vitest)**; S3 e S4 **propostas, não construídas**. O levantamento (`docs/delivery/HANDOFF-QUEUES-SECTORS.md`) deixou cinco decisões ao dono; ele respondeu "Avance" ao ver a recomendação (fila = setor, menor mudança), então ela foi adotada como padrão e **pode ser revista**. As decisões que dependem de conhecimento do negócio (quais setores, por tenant, como os setores internos recebem trabalho) **não foram tomadas**: a operação S1 fica com o dono.
Estado do projeto: `LAB`. Relaciona: ADR-0018 (decisão num ponto só, 404 fora do escopo), ADR-0019 (fluxos: `assign_queue`), ADR-0038 (pools do Hub presumem várias filas por instância).

## 1. Decisão S0 — a fila é o setor

Não se cria a entidade `Department`. Cada tenant cria quantas filas quiser (Comercial, Financeiro, Suporte…); a fila já tem membros, capacidade, modo manual/rodízio e é a unidade que os fluxos (`assign_queue`, `human_handoff`) e o Hub (work pool = "uma fila de uma instância") usam. Uma entidade nova agrupando filas só se justifica se aparecer um setor que **contém várias filas**; até lá seria complexidade sem uso. Na interface o texto continua "grupo" (a tela existente); renomear para "Setor" fica para depois, porque "Grupos" também é a palavra dos grupos de WhatsApp (ADR-0015) e a troca merece uma decisão de vocabulário única.

## 2. S2 — regra de entrada por canal (migration 122)

- `channel_connections.default_queue_id` (nulo = comportamento antigo). Chave estrangeira **composta** `(tenant_id, default_queue_id) → queues(tenant_id, id)`, `ON DELETE RESTRICT`: o banco recusa uma linha apontar para fila de outro tenant e recusa apagar fila para a qual uma linha aponta.
- `RouteNew` escolhe a fila da linha pela qual a conversa chegou; sem regra (ou conversa sem linha), a fila padrão do tenant, como antes. Spam, contato interno e equipe verificada continuam sem fila (nada mudou nessas condições). Conversa que já tem fila **não se move**.
- API (`GET` com `agent.read`; `PUT` com `agent.manage` **e** `channel.manage`, porque a linha é configuração de canal e a política de UPDATE de `channel_connections` só admite o administrador do tenant — o supervisor tem a primeira chave e não a segunda; a resposta nunca é sucesso quando a linha não foi alterada): `GET /tenants/{t}/queues/lines`, `PUT /tenants/{t}/channels/lines/{connection_id}/queue` (`{"queue_id": uuid|null}`). Linha ou fila de outro tenant = 404 indistinguível de inexistente. Auditoria `channel.line_queue_changed` (fila anterior e nova, nenhum conteúdo). `DELETE` de fila com linha apontando = 409.
- Tela: seção "Entrada por canal" na aba Grupos (`LineEntryRules`): um seletor por número; só aparece com linhas e grupos; somente leitura sem `agent.manage`.
- Provas: `TestRouteNewUsesTheQueueOfTheLineTheConversationCameThrough` (linha com setor, sem setor, sem linha, já roteada não move, fila de outro tenant recusada, fila com linha não apaga; **o teste falha com o `RouteNew` antigo**), `TestLineQueueRuleIsPermissionedTenantScopedAuditedAndGuardsDelete`, `LineEntryRules.test.tsx`.
- Revisão do Codex (commit `f7b0918`): CRITICAL/HIGH 0. MEDIUM tratados: sucesso falso para supervisor (agora exige `channel.manage` e confere `RowsAffected`), `DELETE` de fila concorrente devolvia 500 (FK `23503` agora vira 409). MEDIUM **aceito**: a política de gestão do Hub (migration 103, escopo `channels`) pode atualizar qualquer coluna de `channel_connections`, inclusive `default_queue_id` — quem gerencia os canais de uma instância já controla a linha inteira (status, credenciais, desconexão), e a coluna só aceita fila do mesmo tenant pela FK; a rota HTTP nova não é delegável. MEDIUM **aceito com texto ajustado**: o roteamento lê a regra no instante em que a conversa é roteada (mesma transação da entrada), então vale para conversas roteadas depois da mudança.
- Limite: o fluxo (Flow Builder) continua podendo mandar a conversa a outra fila (`assign_queue`) depois da entrada; a regra da linha só decide a fila **inicial**. Regra por assunto/empresa/horário: não construída.

## 3. S3 (proposto) — o agente vê só as filas de que é membro

Hoje o *assumir* é limitado pela fila, mas a **lista** e as rotas que nomeiam a conversa mostram tudo a qualquer agente do tenant. Proposta, no padrão do ADR-0018:
1. Chave `conversation.read_all_queues` (administrador e supervisor a têm; agente e agente do Hub não) e **um único ponto de decisão** (middleware de autorização + o mesmo predicado nas listas), com 404 fora do escopo; sem a chave, o agente vê conversas das filas em que é membro **ativo**, as que atende e as sem fila.
2. **Ligada por tenant** (`tenants.sector_scope_enabled`, padrão desligado): ligar muda o que as pessoas veem, então é decisão do dono por empresa, nunca um efeito colateral de implantar.
3. Hub: o ramo delegado precisa do mesmo predicado (a chave não é delegável por padrão); SSE da conversa e contadores entram no mesmo critério.
4. Testes A/B de tenant, membro revogado, fila trocada, busca, mídia, chamado, histórico do contato, SSE; RLS intacta.
Riscos: contadores e busca global vazam existência se esquecidos; conversas sem fila e atribuídas a quem mudou de setor. **Não construído** — mudança de visibilidade merece a sua decisão por tenant.

## 4. S4 (proposto) — atributos do setor
Descrição, supervisor responsável, horário, SLA, cor e transferência entre setores na tela do tenant. Depende de S3 e de decisões de produto.

## 5. O que falta do dono (S1 e decisões)
Quais setores existem em cada tenant (criar as filas e vincular pessoas é operação, sem código); a fila padrão de `4bae…` (52 conversas numa fila sem membros) e do piloto; como "Servidores", "Outsourcing TI" e "Monitoring" recebem trabalho; ligar ou não o escopo S3 por tenant.
