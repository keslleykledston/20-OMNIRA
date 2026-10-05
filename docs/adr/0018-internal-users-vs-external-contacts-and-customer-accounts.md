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
12. **CRM:** `crm_contact_company_evidence` continua sendo evidência de integração (contato do CRM ↔ empresa), separada do vínculo OMNIRA. Sem `CreateContact` automático no provider sem garantia de idempotência; vários resultados do CRM nunca viram `results[0]`.
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
