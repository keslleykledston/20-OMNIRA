# ADR-0050: Copiloto de grupo e memória de contexto do grupo

## Status
**Proposto em 2026-11-20 por instrução do dono do produto. Nada implementado, nada implantado.** Todas as capacidades nascem atrás de flags **desligadas** e de opt-in por grupo. Decisões já tomadas pelo dono nesta conversa estão marcadas como **(dono)**; o que falta decidir está em "Pendente do dono".

Estende o ADR-0015 (grupos somente leitura) e usa os mesmos princípios dos ADR-0017/0020 (a IA não é autoridade; ela lê e propõe; conteúdo de terceiros é dado não confiável). Referência de conceito: o projeto open source `zap-copilot` (extensão do WhatsApp Web para 1:1). Dele vêm o padrão análise estruturada, sugestão no rascunho, follow-up proposto e aprovado por humano e a trava "não afirmar o que não está na base"; o código não é reaproveitado (ele só lê as 40 últimas mensagens da tela, não tem memória e não trata grupo).

## Contexto

O ADR-0015 entrega a aba Grupos: leitura de grupos habilitados, sem envio, sem fila, sem ticket. Isso mostra o que foi dito, mas não **entende** o grupo. Para ajudar o cliente que vive num grupo, o atendente precisa saber: o que já foi pedido, o que está pendente, o que já foi tratado e o que já foi informado, sem reler centenas de mensagens.

Restrições que vêm do código atual:

- Só os **30 dias quentes** ficam no Postgres; o resto vai para o arquivo frio (HD), com expurgo de 12 meses (ADR-0015, G6). A memória não pode depender de reler o histórico completo toda vez.
- Mensagem de grupo é **entrada de terceiros** (pessoas que não consentiram tratamento individual, ADR-0015 "LGPD") e é vetor de prompt injection.
- A IA da plataforma é o Gemini, por variável de ambiente, com ledger de uso (`ai_usage`). Enviar trecho de grupo ao provedor é decisão de privacidade, não detalhe de código.
- "Ticket" de grupo não pode usar `tickets`, que é preso a `conversation_id` e à regra de um ticket ativo por conversa. O ADR-0049 já tem entidades independentes de conversa: `service_cases` (ocorrência) e `service_tasks` (tarefa, `origin_type = 'ai'` já existe).

## Decisão

### 1. Opt-in de IA por grupo, separado do opt-in de coleta (dono)
`wa_groups` ganha `ai_enabled` (padrão `false`), `ai_enabled_by`, `ai_enabled_at`. Grupo coletado **não** é analisado por IA enquanto um administrador não ligar a IA daquele grupo. A tela explica que trechos do grupo são enviados ao provedor de IA. Auditado em `audit_events`.
Flags globais (todas `OMNIRA_GROUP_*`, padrão off, repassadas no bloco `x-omnira-runtime` do compose): `OMNIRA_GROUP_MEMORY_ENABLED`, `OMNIRA_GROUP_IMPORT_ENABLED`, `OMNIRA_GROUP_PROPOSALS_ENABLED`.

### 2. A memória é um derivado, nunca a fonte de verdade
Tabela `wa_group_memory_items`, escopada por tenant e grupo (RLS `ENABLE` + `FORCE`, mesmo padrão da `000052`). Cada item é tipado:

| `kind` | Exemplo |
|---|---|
| `request` | pedido de alguém, com status `open`/`resolved`/`superseded` |
| `handled_topic` | assunto já tratado e a resposta dada, com data |
| `fact` | informação passada ("o contrato vence dia X") |
| `agreement` | combinado ou padrão do grupo (horário, quem decide) |
| `participant_role` | papel de um autor (cliente, interno, decisor), com vínculo opcional a Contact |

Campos comuns: `summary`, `truth_level` (`said_in_group` · `ai_inferred` · `human_confirmed`, como no ADR-0020), `status`, `pinned`, `relevance`, `last_reinforced_at`, `source` (`live` | `import`), `prompt_version`, `evidence` (ver 3).
Mudanças em item (criar, atualizar, resolver, fixar, apagar) geram linha em `wa_group_memory_item_events` (append-only, `REVOKE UPDATE/DELETE` explícito e gatilho: a migration `000006` concede DML a toda tabela nova, ver ADR-0020).
O humano pode **editar, confirmar, fixar e apagar** qualquer item. Item `human_confirmed` ou `pinned` nunca é sobrescrito pela IA; a IA só propõe versão nova.

### 3. Evidência embutida em cada item
Cada item guarda `evidence`: lista de `{ provider_message_id | import_ref, quote (≤ 200 caracteres), at }`. Assim o item é verificável sem o raw nem o histórico quente. A UI mostra a origem. A IA nunca recebe instrução vinda da `quote`: tudo que vem de grupo entra na **zona não confiável** (`internal/platform/untrusted`), serializado com `json.Marshal`, nunca `%q`.

### 4. Memória em camadas, como cérebro (dono: "memória disponível, tudo é aprendizado")
A memória **não expira por prazo**. Ela é consolidada, não apagada:

- **Camada episódica** (`tier = episodic`): itens recentes, com evidência (`quote`) e autor.
- **Camada consolidada** (`tier = consolidated`): uma rotina periódica (padrão semanal por grupo ativo; também sob demanda) condensa itens antigos ou resolvidos em conhecimento do grupo (padrões, combinados, fatos, histórico de assuntos tratados). O consolidado **não cita trecho literal** e refere pessoas **por papel**, sem telefone nem JID. Os itens episódicos originais ficam `superseded` e perdem a `quote`.
- **Relevância no lugar de esquecimento:** `relevance` decai com o tempo e **sobe** quando o item é consultado, reforçado por nova mensagem ou confirmado por humano. O decaimento só decide **o que entra no prompt**; não apaga nada.
- **Memória consultável por pergunta**, não só injetada: o copiloto tem uma ferramenta de leitura `group.memory.search` (somente leitura, escopada ao grupo, no gateway de ferramentas do ADR-0017; falha limpa quando o grupo não tem `ai_enabled`).
- **Atualização incremental** com `wa_group_memory_state` (`last_processed_at`, `last_processed_message_id`, `last_consolidated_at`, `prompt_version`, uso de tokens). A IA recebe memória atual + mensagens novas e devolve **diffs** (adicionar, resolver, atualizar), nunca reescreve tudo. **Sem chamada por mensagem:** gatilhos são pedido do atendente ("Analisar agora"), menção a palavras de pedido ou ao número da empresa, e lote por volume ou tempo.
- Toda chamada passa pelo ledger de IA, com teto por tenant e por grupo. A chamada ao modelo **nunca** segura trava de linha (lição do ADR-0020).

### 5. Bootstrap: histórico quente e arquivo de contexto (dono)
**5.1 Histórico já guardado.** Leitura inicial em lotes (map-reduce) sobre os 30 dias quentes, com teto de tokens e custo no ledger.

**5.2 Importar arquivo** (export `.txt`/`.zip` do WhatsApp ou backup), pelo painel do copiloto, permissão `group.import`:

- **Entrada segura:** limite de tamanho e de arquivos no zip (proteção contra zip bomb), varredura pelo ClamAV do stack, tipo sniffado. Só texto entra; mídia do export vira marcador tipado (como no ADR-0015). Sem baixar nem executar nada do zip.
- **Parse normalizado e versionado:** os formatos de data do WhatsApp variam por idioma e plataforma (iOS/Android). O parser declara o que não entendeu (mensagens rejeitadas com motivo) e produz JSONL (`autor, data, texto, tipo`).
- **Dedupe** contra o histórico ao vivo por chave `(data, autor, hash do texto)`, porque o export não tem `provider_message_id`.
- **O arquivo importado NÃO é gravado em `wa_group_messages`.** Fica fora da retenção quente/fria do ADR-0015; vira só entrada de extração.

### 6. Raw guardado, imutável, cifrado e descartado por regra mecânica (dono)
Tabela `wa_group_imports`: `sha256`, tamanho, formato detectado, `storage_ref` do arquivo cifrado (AES-256-GCM, padrão do ADR-0009), `normalized_ref` (o JSONL), quem importou, quando, `expires_at`, `discarded_at`, `discard_reason`. O raw é **append-only**: ninguém o edita. A UI **não** lista nem baixa o raw; só o job de reprocessamento o lê. Baixar exige permissão própria e gera `audit_events`.

**Registro de cobertura** por importação, no lugar de uma "garantia": total de mensagens no arquivo, entendidas pelo parse, rejeitadas (com motivo), lotes enviados, lotes concluídos e lotes com falha, intervalo de datas, itens extraídos, `prompt_version`. Cobertura completa significa "100% das mensagens passaram por extração, sem lote falho"; **não** significa "tudo foi capturado", e a UI não diz isso.

**Descarte (dono: prazo de 90 dias):** o raw é descartado quando
(a) a cobertura está completa **e** um humano confirmou "descartar raw", ou
(b) passaram **90 dias** (`expires_at`, configurável por tenant).
Também é descartado por "apagar histórico" do grupo e por desabilitar a IA do grupo (a tela pergunta). O descarte é registrado em `audit_events` com o `sha256`. Como cada item já carrega evidência (seção 3), descartar o raw não deixa referência quebrada.

**Reprocessamento seguro:** com o raw guardado dá para refazer a extração com prompt ou modelo novos. É idempotente por (`import_id`, `prompt_version`); gera **proposta com diff** da memória e não substitui; nunca toca item confirmado ou fixado; passa pelo ledger e pelo teto de custo.

### 7. Privacidade
- Opt-in de IA por grupo (seção 1).
- **Minimização no envio ao provedor:** nomes sanitizados com a regra de `sanitizeSenderName`, telefone e e-mail mascarados antes do envio (opção por tenant, padrão ligado), mídia nunca enviada. O raw guardado fica intacto; só o envio é minimizado.
- Logs nunca registram corpo, JID de participante nem telefone (regra do ADR-0015).
- **Apagar:** são três ações separadas e explícitas, todas auditadas:
  1. *Apagar histórico* (ADR-0015): apaga mensagens, arquivo frio **e o raw**, e remove as `quote` das evidências; o conhecimento derivado permanece.
  2. *Apagar memória do grupo*: apaga todos os itens e eventos do grupo (`group.memory.manage`).
  3. *Desabilitar IA do grupo*: para novas análises; a memória fica disponível para consulta e a tela oferece apagá-la.

### 8. Ticket e follow-up a partir do grupo (dono)
A IA **só propõe**; quem aprova é um humano (padrão do zap-copilot e do ADR-0020). Tabela `wa_group_proposals`: `kind` (`case` | `task`), `payload` validado e limitado, `memory_item_id`, `status` (`pending_approval` → `approved` | `rejected` → `created`), `approved_by`, `created_case_id` / `created_task_id`.

- "Ticket" = **ocorrência** (`service_cases`, ADR-0049). "Follow-up" = **tarefa** (`service_tasks`, `origin_type = 'ai'`). **Nenhuma conversa 1:1 fantasma** é criada e `tickets` não é tocado, então a regra de um ticket ativo por conversa não se aplica.
- O vínculo ocorrência/tarefa ↔ grupo fica em `wa_group_proposals` (as tabelas de `service_*` não ganham coluna). Se M3 mostrar que isso é insuficiente (consulta "tudo que nasceu deste grupo"), a migration extra vai em addendum.
- **Idempotência:** (`memory_item_id`, `kind`) abre no máximo uma proposta ativa; aprovar duas vezes devolve o mesmo resultado.
- **Follow-up só gera tarefa para um atendente.** Não envia mensagem ao grupo. **Envio a grupo continua fora deste ADR** (o `SendText` recusa `@g.us` e exige ADR próprio).
- A UI já pode, a partir da memória, mostrar "pendências abertas" e oferecer "Criar ocorrência" ou "Criar tarefa".

### 9. Permissões (mínimo privilégio)
`group.ai.read` (ver memória e propostas), `group.ai.manage` (ligar IA do grupo, editar, confirmar, fixar, apagar itens, aprovar propostas), `group.import` (importar arquivo), `group.memory.erase` (apagar memória). Concedidas por padrão a `tenant_admin` e `tenant_supervisor`, **não** a `tenant_agent`. Autorização sempre dentro do `TenantContext`; `tenant_id` nunca vem do payload. Segue a delegação do Hub (ADR-0040/0042): um domínio `group_ai` só será delegável depois de gate próprio.

### 10. Expansão para cliente (dono: "por enquanto só grupo, mas pode expandir para cliente")
Este ADR escopa a memória ao **grupo**. O desenho deixa o ponto de extensão sem implementá-lo: `participant_role` pode apontar para Contact, e a camada consolidada pode ser lida por conta/cliente quando o ADR-0018 (accounts) permitir. **Não** há memória cruzada entre grupos nem entre tenants; unir grupo a cliente exige ADR próprio, com a regra de escopo por contato do ADR-0017 (um vazamento entre clientes já aconteceu em teste e foi corrigido).

## Plano em fatias
Todas atrás de flag desligada; Postgres real e Tenant A/B em cada uma.

- **M0 — Modelo e permissões:** migrations (`ai_enabled`, `wa_group_memory_items`, `wa_group_memory_item_events`, `wa_group_memory_state`), RLS forçado, permissões, contrato OpenAPI. A próxima migration livre no `main` é `000169`; confirmar contra as faixas reservadas das branches antes de numerar.
- **M1 — Memória a partir do histórico quente:** extração em lotes, diffs, versão de prompt, ledger, ferramenta `group.memory.search`, painel do copiloto só leitura (visual aprovado pelo dono antes do deploy).
- **M2 — Importar arquivo:** `wa_group_imports`, parser versionado, ClamAV, cobertura, cifra, descarte (90 dias), reprocessamento com diff.
- **M3 — Propostas:** `wa_group_proposals`, aprovação humana, criação de `service_cases`/`service_tasks`.
- **M4 — Consolidação e apagar:** camada consolidada, decaimento/reforço de `relevance`, as três ações de apagar e a ligação com a retenção do ADR-0015.
- **Depois, com ADR próprio:** envio a grupo, memória por cliente/conta.

## Consequências

- Ganho: o atendente vê pendências, histórico tratado e fatos do grupo sem reler o grupo; o ticket e o follow-up nascem do grupo sem tocar o modelo 1:1.
- **Tensão com a retenção do ADR-0015 (registrada, decisão do dono):** o dono quer a memória disponível mesmo depois que as mensagens saem do banco. A memória sobrevive às mensagens, e isso é dado pessoal derivado. Mitigação: camada consolidada sem trecho literal e com papéis em vez de pessoas, `quote` removida ao consolidar ou ao apagar histórico, e três ações de apagar explícitas e auditadas. A tela da aba Grupos precisa dizer isso.
- Custo: três tabelas de memória, duas de import/proposta, quatro permissões, chamadas de IA com teto e ledger.
- **Segurança:** conteúdo de grupo é hostil por padrão (injeção de prompt); a IA nunca executa ação, só propõe; escrita de memória por IA fica restrita a itens não confirmados. Testes obrigatórios: Tenant A/B com acesso cruzado em leitura e escrita; membership revogada; injeção de prompt em mensagem de grupo e em arquivo importado (a cerca vence); zip malicioso e zip bomb; arquivo com formato de data desconhecido; item `human_confirmed` imune a reprocessamento; aprovação duplicada idempotente; descarte do raw com cobertura incompleta recusado.
- **Observabilidade:** contadores por tenant de lotes, itens criados e consolidados, propostas aprovadas e rejeitadas, importações e descartes, tokens e custo por grupo.
- **Reversibilidade:** migrations têm `down`; desligar as flags e `ai_enabled` volta ao comportamento do ADR-0015.
- Gate de linguagem: este ADR e suas fatias não autorizam dizer "pronto/produção/E2E passou"; usar IMPLEMENTED, NOT WIRED, UNIT/POSTGRES/HTTP/E2E VERIFIED, BLOCKED.

## Alternativas descartadas

- **Memória como resumo único em texto:** vira telefone sem fio (resumo do resumo) e não permite consulta nem conferência. Rejeitada em favor de itens tipados com evidência.
- **Reler o histórico a cada pergunta:** custo alto, e o histórico frio não está no banco. Rejeitada.
- **Guardar o arquivo importado em `wa_group_messages`:** mistura com a retenção quente/fria e com mensagem ao vivo duplicada. Rejeitada.
- **Descartar o raw sempre, sem guardar:** impede reprocessar quando o prompt melhorar, e não há como provar que a memória cobre tudo. Rejeitada em favor de raw cifrado com prazo.
- **Prometer "cobertura total":** a IA extrai o que parece relevante; só dá para medir que tudo foi **lido**. Rejeitada.
- **Expirar a memória por prazo:** o dono quer a memória como aprendizado contínuo. Rejeitada; no lugar, consolidação e relevância.
- **Ticket de grupo em `tickets` / conversa fantasma:** quebra a regra de um ticket ativo por conversa e traz grupo à fila de clientes. Rejeitada em favor de `service_cases`/`service_tasks`.
- **Copiloto responder direto no grupo:** amplia escopo e risco (mensagem pública irreversível). Rejeitada; só sugestão no painel e proposta aprovada por humano.
- **Extensão de navegador como no zap-copilot:** o OMNIRA tem servidor, RLS e ledger; ler a tela do WhatsApp Web perderia tenancy, auditoria e escala. Rejeitada.

## Pendente do dono
1. Quem pode confirmar o descarte do raw além de `tenant_admin`/`tenant_supervisor`? (Proposta: só eles.)
2. Frequência padrão da consolidação (proposta: semanal por grupo ativo) e teto de tokens por grupo.
3. "Apagar histórico" mantém o conhecimento derivado (proposta atual) ou apaga também a memória? Envolve LGPD; confirmar antes da M4.
4. Máscara de telefone/e-mail antes de enviar ao provedor: padrão ligado (proposta) ou desligado?
5. Visual do painel do copiloto de grupo: aprovação do dono antes de qualquer deploy (regra permanente de UI).
