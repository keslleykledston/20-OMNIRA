# ADR-0013: ERP-Authoritative Ticketing with OMNIRA Local Projection

## Status
Accepted

## Contexto

O PRODUCT.6 (Operational Capability Prioritization Gate) encontrou um
problema crítico ao investigar o próximo slice operacional: o `TicketPanel`
(UI de Inbox) executa CRUD de ticket contra
`internal/inbox/adapters/crm_handlers.go` → `connectors.CRMConnector`, cuja
única implementação alguma vez instanciada em runtime era
`connectors.NewMockCRMConnector()` — em memória, por processo, não durável,
e completamente desconectada da tabela Postgres `tickets` que `/tickets`
(PRODUCT.2-B) e a exportação CSV (PRODUCT.5-A) usam. Isso configurava duas
"verdades" de ticket incompatíveis coexistindo no mesmo produto.

O PRODUCT.6-A (Ticket Authority & ERP Integration Architecture Gate)
investigou a causa raiz e recebeu uma correção explícita de produto: OMNIRA
**não** deve se tornar o sistema de chamados autoritativo para operação de
clientes. Clientes operam ERPs de mercado (SGP, HubSoft, IXC, Topsapp,
MikWeb, MK-Auth, K3G CRM, entre outros) que já são autoridade sobre o ciclo
de vida do chamado de atendimento.

O PRODUCT.6-B conteve o caminho falso (mock nunca mais é instalado em
runtime de produção/piloto; toda mutação de ticket via `TicketPanel` retorna
503 explícito até existir um conector real por tenant).

O PRODUCT.6-C investigou o que existe hoje para viabilizar um conector real
com K3G, primeiro tenant e primeiro provedor-alvo, e confirmou:

- `internal/tool/connectors/k3gcrm.go` (`K3GCRMClient`) só implementa
  operações de relacionamento/CRM (`ListCompanies`, `FindCustomerByPhone`,
  `CreateContact`, `CreateActivity`) — comentário do próprio arquivo declara
  "Não há /api/crm/tickets: este CRM registra relacionamento, não chamado."
  Não deve ser tratado como conector de ticket.
- Não existe no repositório nenhuma especificação de API de chamados K3G
  (endpoint, contrato de request/response, vocabulário de status,
  autenticação, idempotência) — apenas menções de produto genéricas em
  `docs/product/MVP.md` e `docs/delivery/DELIVERY-SLICES.md`.
- `internal/tool/connectors/ixc.go` (`IXCConnector`) já implementa a
  interface `connectors.CRMConnector` completa contra a tabela real do IXC
  (`su_oss_chamado`), mas o próprio arquivo marca cada decisão como
  "SUPOSIÇÃO" — nunca foi exercido contra um ambiente IXC real. Serve como
  prova de que a interface é implementável por um segundo provedor, não como
  especificação de nenhum provedor real.

## Decisão

**Autoridade de registro:**

- OMNIRA é fonte da verdade para: conversas, mensagens, estado de
  interação/roteamento.
- O ERP de cada tenant é fonte da verdade para: o ciclo de vida do chamado
  de atendimento (ticket/chamado).
- O registro `tickets` do PostgreSQL do OMNIRA evolui em direção a um
  **vínculo/projeção local** entre conversa e ticket externo — visibilidade
  operacional unificada, não autoridade concorrente de help desk.

**Primeiro provedor:**

K3G é o primeiro tenant e primeiro provedor-alvo de integração de
ticketing. K3G-primeiro **não** significa K3G-hardcoded: a fronteira de
ticketing deve permanecer neutra a provedor, para que IXC, SGP, HubSoft,
Topsapp, MikWeb, MK-Auth e outros possam satisfazer o mesmo contrato no
futuro. `K3GCRMClient` não é declarado conector de ticket — ele continua
sendo exclusivamente o cliente de relacionamento/CRM K3G.

**Problema de runtime atual:**

O `TicketPanel` usava `MockCRMConnector` (memória, por processo, não
durável, não visível em `/tickets`) como autoridade de fato. Isso não pode
permanecer em runtime de produção/piloto. O slice de implementação imediato
é PRODUCT.6-B — Contain Fake TicketPanel Ticket Mutations (já concluído:
`NewCRMHandlers` não instala mais nenhum conector por padrão; toda rota de
mutação de ticket retorna 503 "ticketing integration not configured for
this tenant" até existir conector real configurado por tenant).

**Tickets locais atuais:**

São reais, duráveis e protegidos por RLS — mas **transicionais** em relação
à arquitetura-alvo. Hoje não têm `provider`, `external_ticket_id`,
`external_status` nem estado de sincronização. Não devem ser chamados de
"tickets ERP-backed". Linhas existentes não devem ser apagadas; a intenção
futura é evoluí-las aditivamente para vínculos/projeções de ticket externo.

**Criação automática de ticket em mensagem inbound:**

`internal/inbox/application/inbound.go` cria um ticket local
automaticamente a cada mensagem inbound nova. Esse comportamento é
transicional. Conversa != Ticket. A arquitetura-alvo não deve assumir que
toda conversa inbound precisa gerar um ticket ERP; mudar esse
comportamento requer um slice de produto explícito futuro, não decidido
por esta ADR.

**Candidatos de projeção futura (não implementados, não são contrato de
schema fechado):**

- `provider TEXT`
- `external_ticket_id TEXT`
- `external_status TEXT`
- `sync_status TEXT`
- `last_synced_at TIMESTAMPTZ`

Unicidade candidata: `UNIQUE (tenant_id, provider, external_ticket_id)`. O
schema final fica sujeito ao contrato de API K3G validado — nenhum desses
campos é adicionado por esta ADR.

**Requisitos de consistência (congelados como requisito, não como
mecanismo):**

- criação de ticket externo deve ser idempotente/retry-safe;
- confirmação do ERP precede tratar a projeção como sincronizada;
- falha de projeção após sucesso no ERP deve ser reconciliável;
- indisponibilidade do ERP nunca deve ser representada como criação de
  ticket bem-sucedida;
- retries não podem criar tickets duplicados no ERP;
- o estado local deve expor a verdade de sincronização honestamente (nunca
  fingir "sincronizado" sem confirmação).

Síncrono vs. assíncrono e uso de outbox **não** são decididos por esta ADR
— a estratégia de execução será escolhida somente após o contrato real da
API de ticket K3G ser conhecido.

**Bloqueio de implementação:**

A implementação de ticketing real com K3G está **bloqueada** até existir
contrato de API de chamados validado. Evidência necessária: documentação
de endpoint/OpenAPI ou ambiente de sandbox, requisitos de autenticação,
identificador de cliente/assinante, criação/leitura de ticket, operações
de update/close suportadas, vocabulário de status, semântica de erro,
comportamento de idempotência, credenciais de teste. Nenhum endpoint ou
campo deve ser inventado — nem mesmo seguindo o precedente do IXC.

**IXC:**

`IXCConnector` não é evidência de produção validada. Demonstra apenas que
o formato de interface atual (`connectors.CRMConnector`) é implementável
por um segundo provedor. Suas suposições não devem ser usadas como
especificação para K3G ou qualquer outro ERP real.

## Consequências

- `TicketPanel` permanece em estado de containment honesto (503 explícito)
  até um conector real e configurado por tenant existir — nenhuma regressão
  para "sucesso fake".
- `/tickets`, exportação CSV e a tabela `tickets` continuam funcionando
  exatamente como hoje; nada nesta ADR os modifica.
- A implementação de um conector K3G de ticketing real fica bloqueada até
  recebermos o contrato de API de chamados K3G (endpoints, auth,
  identificador de cliente, vocabulário de status, idempotência,
  credenciais de teste).
- Migração de campos de projeção (`provider`, `external_ticket_id`, etc.) é
  trabalho futuro, condicionado ao contrato K3G confirmado — não faz parte
  desta ADR.
- Mudar a criação automática de ticket em `inbound.go` requer um slice de
  produto explícito futuro; esta ADR não a altera nem a autoriza a mudar
  implicitamente.

## Não-objetivos

- Não implementa integração ERP real (K3G, IXC ou qualquer outro).
- Não adiciona campos de projeção ao schema `tickets`.
- Não altera runtime de produto (backend ou frontend) além do que
  PRODUCT.6-B já entregou.
- Não decide mecanismo de consistência (outbox, retry, síncrono vs.
  assíncrono) — apenas os requisitos que qualquer mecanismo escolhido
  precisará satisfazer.
- Não altera o comportamento de criação automática de ticket em mensagem
  inbound.
