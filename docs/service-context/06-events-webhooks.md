# 06 — Eventos e webhooks

## O que existe (entregue)

- **Diário durável imutável**: `context_events` (migration 000151). Cada fato é escrito **na mesma transação** da mudança que ele descreve (não existe caminho de escrita que esqueça o evento). Identificador estável (`event_id`), empresa, versão do esquema, `occurred_at`/`recorded_at`, correlação/causalidade, entidade, vínculos, ator e payload (ids, enums e contagens; ≤ 2 KB; **nunca** texto de mensagem, de cliente ou a nota digitada por uma pessoa).
- **Envelope e nomes públicos versionados**: `domain.Envelope` / `domain.PublicEvent`; contrato `contracts/asyncapi/service-context-events-v1.yaml` + `contracts/jsonschema/service-context-event.v1.schema.json`. Os nomes propostos no pedido existem: `service_session.opened|classified|closed.v1`, `service_case.created|updated|resolved.v1`, `service_task.created|completed.v1`, `external_record.linked|status_changed.v1` (+ os extras: reaberta, atribuída, vínculo de sessão, solicitação ao ERP…). Um teste compara o que o código **pode anunciar** com o que o contrato **documenta**, nos dois sentidos.
- **Entrada**: `Service.ApplyExternalUpdate` aplica uma atualização empurrada pelo ERP — idempotente por *delivery id* (recibo), segura contra **ordem trocada** (o tempo do próprio provedor só avança), nunca cria registro, só em sessão de sistema (a camada de integração já autenticou e resolveu a empresa a partir da integração armazenada, **nunca** do payload).

## Gate 4 — entrega a aplicações externas (entregue em 2026-10-17, migration 164)

A branch headless (ADR-0048) foi mesclada em `main` (migrations 159–163). O módulo agora entrega seus fatos por ela, **sem reescrever o serviço**:

- **Fonte única**: um gatilho `AFTER INSERT` em `context_events` escreve o fato em `domain_events` **na mesma transação**, com o nome público do módulo (`domain.PublicEvent`; o mapeamento em SQL é conferido contra o Go por teste, nos dois sentidos, para os 19 tipos × cada valor de `to` que muda o nome). São 20 nomes públicos (`service_session.*`, `service_case.*`, `service_task.*`, `external_record.*`, `external_request.*`).
- **Escopo novo `service.read`** na instalação da aplicação externa. Sem ele: nada é gravado no diário se nenhuma instalação da empresa o tem; e a instalação sem o escopo **nunca** recebe esses fatos, nem pela API de eventos nem por webhook.
- **Visibilidade**: um fato ligado a uma conversa segue a visibilidade da conversa e os limites de fila/linha da instalação (conversa de equipe, contato interno ou spam: silêncio). Um fato **sem** conversa (ocorrência, tarefa, registro do ERP) só vai a instalação **sem limite** (não dá para situá-lo numa fila). A visibilidade é perguntada de novo na leitura e no envio; retirar o escopo cancela as entregas pendentes (`not_visible`).
- **Assinatura de webhook**: só para instalação com `service.read` (422 caso contrário); nome desconhecido = 400.
- **Conteúdo**: `data` leva `links` (ids), `actor_type` e o payload do módulo (ids, enums, contagens; se não couber em 2 KB, só links e ator). A nota digitada por uma pessoa nunca sai.
- **Contratos**: JSON Schema, AsyncAPI e OpenAPI externo descrevem os 20 tipos e o escopo; 20 amostras validadas pelo teste de contrato.
- **Testes** (PostgreSQL real): nomes sem deriva; só com o escopo; empresa A nunca ouve a B; instalação limitada a uma fila; contato que vira interno depois do fato; assinatura sem escopo; entrega assinada sem conteúdo; escopo retirado antes do envio (mutações verificadas nas três portas).

## O que continua fora

| Item do pedido | Estado | Motivo |
|---|---|---|
| Receptor HTTP de webhook **do ERP** (entrada) | **não construído** | Precisa de identidade de integração verificada (assinatura) e do contrato do provedor (`16`); o caso de uso `ApplyExternalUpdate` está pronto e testado para ser chamado por ele. |
| Fatos de ocorrência/tarefa em instalação com limite de fila | por desenho | Sem conversa não há como situar na fila; só instalação sem limite os recebe. |

Enquanto isso, o diário é consultável pela API (`…/timeline`) e pelo contexto do cliente.
