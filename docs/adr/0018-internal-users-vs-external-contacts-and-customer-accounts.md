# ADR-0018: Usuários internos × contatos externos × contas de cliente

## Status
Accepted (2026-10-05). Implementação por ondas; as automações novas nascem desligadas ou restritas a identidades verificadas.

## Contexto
Estado verificado em 2026-10-05 (HEAD `3bbb879`, schema `000072`):
- `users` (420 linhas, sem telefone) + `memberships` (5) + papéis/permissões. `user_identities` é só federação OIDC (issuer/subject), **não** identidade de canal.
- `contacts` (122, todos `kind='other'` por padrão) com `kind IN ('customer','other','spam','agent')` (ADR-0014; `agent` foi acrescentado em 2026-10-05 e é um **erro de modelagem**: agente é User, não Contact).
- Não existe domínio real de empresa/cliente atendido. `web/src/pages/Accounts.tsx` é mock legado ("contas BPO").
- `crm_contact_company_evidence` (migration 000052; 0 linhas) guarda evidência de que um Contact OMNIRA foi associado a uma empresa externa, nascida de criação de ticket; **não** é classificação.
- O ticket recebe `SelectedCustomerExternalID` do navegador e o valida contra o `CompanyDirectory` (K3G `ListCompanies`) do tenant; `external_company_id` é por provider/conexão.
- Conversas/grupos podem misturar agentes e clientes; hoje nada separa "conversa interna" de "atendimento".

## Decisão
**Separar quatro conceitos, sem unificar persistência:**
| Conceito | Papel |
|---|---|
| `User` (+ membership/role) | operador interno; autoridade de acesso só por RBAC |
| `Contact` | pessoa externa |
| `CustomerAccount` | organização atendida (identidade local e estável) |
| `ContactAccountLink` | relação N:N da pessoa com a organização |

1. **Nunca** `contacts.company_id`, `is_internal`, `is_agent`, regra por domínio de e-mail ou papel hardcoded. Um Contact pertence a várias empresas.
2. **`customer_accounts`** (nova; não havia domínio real a reutilizar) + **`account_external_links`** (identidade da empresa por provider/conexão, `UNIQUE(tenant, provider, connection, external_company_id)`). O K3G `external_company_id` não é a identidade do produto.
3. **Classificação do Contact reaproveita `contacts.kind`** (já é a classificação do ADR-0014; não se cria coluna paralela): valores `unclassified` (novo padrão), `customer`, `other` e `spam` (estado de segurança ortogonal, mantido). `agent` é **retirado** (migra para `other`, auditado). Novas colunas `classification_source`, `classified_at`, `classified_by_user_id`. A API continua expondo `kind` e acrescenta a origem.
4. **Invariante de domínio:** `customer` ⇒ ≥ 1 `contact_account_links` ativo. Garantida por transação (serviço) **e** por gatilho de restrição adiado no banco. A transição `→ customer` e a criação do vínculo são atômicas. Remover o último vínculo de um customer só junto com reclassificação.
5. **Vínculos nunca somem fisicamente** (`status`, `ended_at`); no máximo um `is_primary` ativo por contato; `primary ≠ exclusivo`.
6. **Seleção de empresa vinda do `CompanyDirectory`** é revalidada no backend (a empresa/CNPJ/status do navegador nunca são autoridade) e materializada em `CustomerAccount` + `AccountExternalLink`, em transação. Empresa inativa no provider não apaga o histórico; bloqueia apenas **novo** write no provider.
7. **Identidade interna em canal** (`user_channel_identities`: `pending|verified|revoked`) é fronteira de segurança: só `verified` participa do resolvedor; nunca por nome, telefone inferido ou IA; **não concede permissão** (RBAC continua sendo a autoridade). Unicidade da identidade verificada por tenant; conflito com Contact existente vira `identity_resolution_conflicts` (nunca resolvido em silêncio; automação de cliente suspensa para essa identidade). Permissão dedicada `identity.manage`.
8. **Ordem do resolvedor de entrada:** normalizar → buscar identidades internas **verificadas** → detectar conflito → (ator interno único) ou resolver Contact externo → criar/reusar `unclassified`. Em grupo, cada participante é resolvido individualmente.
9. **`conversations.conversation_kind`** (`internal | customer_service | external_other | unclassified`), determinística: todos os participantes humanos conhecidos são Users verificados → `internal`; ≥ 1 customer → `customer_service` (agentes presentes não a tornam interna; `has_unclassified_participants` sinaliza); sem customer e só `other` → `external_other`; qualquer externo sem classificação → `unclassified`. Recalculada na mudança de classificação; auditada. Mensagem enviada por User numa conversa de cliente continua sendo atendimento (não é "nota interna").
10. **Contexto de empresa pertence ao assunto, não à conversa:** nunca `conversation.account_id`. Com TopicThread (existe), `topic_account_links` (`primary|related`); contato com N empresas **não é adivinhado**: sem evidência (empresa citada, entidade, tópico atual, resposta, handoff, escolha explícita) pergunta-se e não se abre ticket. IA só sugere; confirmação humana.
11. **Ticket:** `tickets.customer_account_id` (local); a empresa do provider é resolvida via `AccountExternalLink`, com revalidação no `CompanyDirectory`. `unclassified`/`other` não geram ticket de cliente automático.
12. **CRM:** `crm_contact_company_evidence` continua sendo evidência de integração (contato OMNIRA ↔ empresa do provider, nascida de uma seleção de ticket validada), separada do vínculo OMNIRA. A evidência é **somente sugestão**: ela não guarda nome da empresa (só o id do provider), então não há materialização automática de conta/vínculo (nem em backfill; hoje 0 linhas) — uma pessoa aceita a sugestão e o servidor revalida a empresa no `CompanyDirectory`; o vínculo nasce com `source=ticket_flow` e a decisão de classificar continua `manual`. Sem `CreateContact` automático no provider (flag `OMNIRA_CRM_AUTO_CONTACT_CREATION_ENABLED`, desligada) sem garantia de idempotência; vários resultados do CRM nunca viram `results[0]` (`ErrCRMAmbiguous`: nada é vinculado nem criado).
13. **Automação de cliente (bot/SLA/CSAT/sugestão de ticket) só em `customer_service`;** `internal` não entra em métricas de atendimento.
14. **Backfill seguro:** nenhum contato vira customer sem prova. Contatos `other` sem classificação manual (sem `contact.kind_changed` na auditoria) viram `unclassified` (`source=migration`); `customer` sem vínculo é rebaixado a `unclassified`; evidência CRM ativa inequívoca (hoje 0) pode materializar conta + vínculo (`trusted_crm`). Telefone de User não vira identidade verificada.

## Consequências
- Quatro tabelas novas de domínio + identidades internas + conflitos; colunas novas em `contacts`, `conversations` e `tickets`; permissões novas (`contact.classify`, `account.read`, `account.manage`, `identity.manage`); flags independentes (`contact_classification`, `customer_accounts`, `internal_channel_identity`, `conversation_kind`).
- O endpoint antigo de classificação de contato passa a exigir empresa ao classificar como cliente.
- Compatibilidade: `kind`/`contact_kind` seguem nos contratos atuais; o valor `agent` some.

## Alternativas rejeitadas
`contacts.company_id` (um contato, várias empresas); `external_company_id` como identidade central; usar `Contact` para pessoas internas; `conversation.account_id`; inferir identidade interna por domínio de e-mail ou por IA; auto-merge de contatos por similaridade; unificar Users e Contacts numa só tabela.

## Ondas
0 ADR → 1 contas de cliente → 2 classificação + vínculos → 3 API/UI de edição → 4 identidades internas → 5 resolvedor + `conversation_kind` → 6 evidência CRM → 7 ticket → 8 assunto (`topic_account_links`) → 9 diretório de pessoas e filtros → 10 métricas/documentação. Um commit local por onda verde; nada vai a produção sem autorização.

## Adendo 2026-10-07 — contato declarado "Interno" (equipe, parceiro, fornecedor)

**Pedido do dono:** a classificação de pessoa (Cliente, Outros) ganha **Interno**, para separar a comunicação interna — equipe e parceiros/fornecedores — de cliente.

**O que já existia e continua valendo.** `conversation_kind = internal` é a conversa 1:1 com um **User verificado** (`internal_user_id`, sem Contact); a decisão 7 e a 9 não mudam. Quem escreve de um número pessoal sem identidade verificada é um **Contact**, e parceiro/fornecedor é externo de fato: nenhum dos dois é "interno" no sentido de segurança do ADR.

**Decisão (opção C).**
1. Novo valor **`contacts.kind = 'internal'`** (migration `000090`, aditiva e reversível) e coluna **`contacts.internal_role`** (`team | partner | supplier`), com CHECK `(kind = 'internal') = (internal_role IS NOT NULL)`. O papel existe exatamente enquanto o tipo é `internal`; sair de `internal` o apaga.
2. **É declaração de um operador, não identidade.** Não concede permissão nem confiança (decisão 7 intacta). O único efeito é **desligar a automação de cliente** (decisão 13: bot, chamado automático, SLA, CSAT).
3. **A conversa continua `external_other`.** `contact_kind_to_conversation_kind` e a regra de grupo já mapeiam todo tipo que não é `customer` nem `unclassified` para "outros"; a restrição `conversations_internal_kind_chk` (`internal` ⇔ `internal_user_id`) **não é relaxada**. Em consequência a conversa segue na fila, pode ser atribuída e aparece em "Todas".
4. **O filtro "Internas" do Inbox** (`conversation_kind=internal`) passa a devolver a união: conversas com equipe verificada **e** conversas de contatos declarados internos. Cada conversa traz `contact_internal_role`; a lista mostra o selo "Equipe", "Parceiro" ou "Fornecedor" no lugar de "Outros". A visão "Internos" do diretório de pessoas (`view=internal`) traz Users (precisa de `membership.read`) **mais** esses contatos; sem a permissão volta só a parte de contatos (200, nunca a equipe).
5. **Proteção contra marcar cliente como interno** (ele perderia bot, chamado e SLA em silêncio): só quem tem `contact.classify`; **somente `source = manual`** (regra, importação, CRM, fluxo e sugestão de IA são recusados com 400); o papel é obrigatório (422 sem ele, 400 em tipo que não é `internal`); a tela mostra o que a escolha desliga antes de salvar; auditoria `contact.classified/reclassified` com `internal_role_from/to`.
6. `PUT /contacts/{id}/kind` (endpoint simples) **recusa** `internal` (400): só a API de classificação, que carrega o papel, o aceita.

**Rejeitado:** (a) reaproveitar `conversation_kind = internal` para contatos — quebraria a invariante "interno = verificado", faria conversas de fornecedor sumirem de "Todas" e as tiraria da fila; (b) tipo novo de conversa — muita superfície para o mesmo efeito; (c) deixar IA/regra classificar como interno — falha silenciosa para cliente.

**Adiado (não implementado):** vincular um contato "Equipe" a um User (converter em conversa interna verificada, o que exige migrar o histórico entre `contact_id` e `internal_user_id`) e a sugestão automática quando o telefone bater com um User; restringir por permissão quem vê conversas internas.

