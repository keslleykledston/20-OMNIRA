# ADR-0014: Classificação de contatos (`customer` / `other` / `spam`)

## Status
Proposed (guardado para o futuro; nenhuma implementação autorizada)

## Contexto

O Inbox trata toda conversa recebida como se viesse de um cliente. Hoje:

- O webhook WAHA descarta grupos (`@g.us`) e `status@broadcast` por completo
  (`internal/channels/adapters/waha/webhook.go`, ~linha 200). O modelo é "um
  Contact e uma Conversation por remetente 1:1", e o `from` de uma mensagem de
  grupo é o JID do grupo, não de uma pessoa.
- Não existe nenhum campo de classificação. Em `omnira_dev` (2026-10-03):
  118 contatos, todos `status = 'active'`, e 123 conversas, todas `open`.
  Quase todas as conversas vêm de ids `@lid`; o que for spam, fornecedor,
  conhecido ou cliente entra igual e disputa a fila de roteamento.
- `contacts.status` descreve o ciclo de vida do registro, não quem a pessoa é
  para o negócio. Não deve ser reaproveitado.
- Não há permissão `contact.*`. As existentes relevantes são
  `conversation.manage` e `conversation.claim`.
- O vínculo automático Contact ↔ CRM externo (K3G) foi considerado inseguro na
  PRODUCT.7B2C e segue bloqueado pela pergunta de unicidade enviada ao time K3G.
  Qualquer regra que decida "é cliente" consultando o CRM herda esse bloqueio.

Necessidade do produto: separar (1) grupos de clientes, (2) contatos de
cliente e (3) "Outros" (conversas diversas e spam), para que a fila de
atendimento e as métricas reflitam só o que é atendimento de cliente.

## Decisão

Proposta, em fatias pequenas, nesta ordem:

1. **Slice 1 — classificação manual de contato.** Nova coluna
   `contacts.kind` com valores `customer`, `other`, `spam`, `NOT NULL`,
   default `other`. Migration forward-only, com backfill explícito e
   reproduzível: contatos existentes ficam `other`, exceto os que já tenham
   evidência em `crm_contact_company_evidence` (migration 000052), que viram
   `customer`. Nada é inferido por heurística de texto ou de número.
2. **Triagem no Inbox.** Ação "Marcar como Cliente / Outros / Spam" sobre o
   contato da conversa. Autorização inicial reaproveita `conversation.manage`;
   uma permissão própria (`contact.manage`) só entra se o produto pedir papéis
   distintos. Cada mudança grava `audit_events` (quem, de, para) e emite o
   evento de realtime já existente para a conversa.
3. **Efeito de `spam`.** A mensagem continua sendo persistida (dado nunca é
   descartado em silêncio e o webhook segue em exactly-once), mas a conversa
   não entra em roteamento, e "sem agente elegível" continua sendo ACK de
   condição normal, nunca NAK/redelivery. `other` aparece em aba própria e não
   conta nas métricas de atendimento.
4. **Grupos ficam fora desta ADR.** O webhook continua descartando `@g.us`.
   Suportar grupos exige decisão própria: entidade de grupo separada de
   Contact, remetente individual por mensagem e política de roteamento
   específica. Tratado no ADR-0015 (leitura de grupos em área própria,
   sem reutilizar `contacts`).
5. **Sem classificação automática no início.** Regras automáticas (por CRM,
   por padrão de conteúdo, por reputação do número) só depois de: resposta do
   K3G sobre unicidade, e medição de falso positivo sobre dados reais. Quando
   existirem, devem apenas **sugerir**, nunca sobrescrever uma classificação
   humana.

## Consequências

- Ganho: fila e métricas limpas com uma coluna e uma ação de UI; mudança
  pequena, reversível por migration down e sem tocar o fluxo de entrega.
- A coluna fica em `contacts` (por remetente), não em `conversations`: a mesma
  pessoa não vira cliente numa conversa e spam em outra.
- Exige testes com tenancy adversarial em Postgres real: Tenant A e Tenant B,
  tentativa de acesso cruzado em leitura e escrita, membership revogada, RLS
  e FORCE RLS intactos na tabela `contacts`.
- `tenant_id` nunca vem do payload; a ação resolve o contato dentro do
  TenantContext.
- OpenAPI e AsyncAPI precisam refletir o novo campo e o evento.
- Telemetria: contador de reclassificações por `kind` e latência da ação.
- Risco: marcar um cliente real como spam silencia o atendimento dele. Mitigar
  com reversão em um clique, auditoria e aba visível de spam (nada some).
- Contatos `@lid` não têm telefone resolvido; a classificação vale para o
  contato como está e não depende de normalização de número.

## Alternativas

- **`kind` em `conversations`:** mais simples de listar, mas descola da
  identidade e obriga reclassificar a cada nova conversa do mesmo remetente.
  Rejeitada.
- **Tags livres por contato:** flexível, porém sem semântica para roteamento e
  métricas; vira dívida. Pode vir depois, em cima do `kind`.
- **Classificar por CRM (K3G) automaticamente:** bloqueado pela pergunta de
  unicidade da PRODUCT.7B2C; "primeiro resultado vence" foi rejeitado
  explicitamente.
- **Módulo completo com grupos já na primeira entrega:** amplia o escopo do
  MVP sem decisão sobre o modelo de grupo. Rejeitada.
- **Deixar como está e filtrar por busca:** não protege a fila de roteamento
  nem as métricas. Rejeitada.

## Questões em aberto

- O produto quer papéis distintos para triagem (supervisor vs agente)?
- Spam deve ser purgado após N dias ou mantido indefinidamente (LGPD)?
- Resposta do K3G sobre unicidade de `GET /api/crm/contacts?phone=&companyId=`.
- Grupos de clientes: um grupo é um Contact especial ou uma entidade nova?
  Respondida no ADR-0015 (proposta): entidade nova, fora de `contacts`.
