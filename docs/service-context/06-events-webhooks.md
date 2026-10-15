# 06 — Eventos e webhooks

## O que existe (entregue)

- **Diário durável imutável**: `context_events` (migration 000151). Cada fato é escrito **na mesma transação** da mudança que ele descreve (não existe caminho de escrita que esqueça o evento). Identificador estável (`event_id`), empresa, versão do esquema, `occurred_at`/`recorded_at`, correlação/causalidade, entidade, vínculos, ator e payload (ids, enums e contagens; ≤ 2 KB; **nunca** texto de mensagem, de cliente ou a nota digitada por uma pessoa).
- **Envelope e nomes públicos versionados**: `domain.Envelope` / `domain.PublicEvent`; contrato `contracts/asyncapi/service-context-events-v1.yaml` + `contracts/jsonschema/service-context-event.v1.schema.json`. Os nomes propostos no pedido existem: `service_session.opened|classified|closed.v1`, `service_case.created|updated|resolved.v1`, `service_task.created|completed.v1`, `external_record.linked|status_changed.v1` (+ os extras: reaberta, atribuída, vínculo de sessão, solicitação ao ERP…). Um teste compara o que o código **pode anunciar** com o que o contrato **documenta**, nos dois sentidos.
- **Entrada**: `Service.ApplyExternalUpdate` aplica uma atualização empurrada pelo ERP — idempotente por *delivery id* (recibo), segura contra **ordem trocada** (o tempo do próprio provedor só avança), nunca cria registro, só em sessão de sistema (a camada de integração já autenticou e resolveu a empresa a partir da integração armazenada, **nunca** do payload).

## O que NÃO foi entregue e por quê

| Item do pedido | Estado | Motivo |
|---|---|---|
| Entrega de webhook assinado a terceiros | **BLOCKED (dependência)** | O motor existe na branch headless (HMAC v1, SSRF guard, backoff 5 s→1 h por 72 h, *dead letter*, `FOR UPDATE SKIP LOCKED`), ainda não integrada ao `main`. Duplicá-lo violaria "não duplicar". Em `main` o publisher só tem *stream* para `job.*`. |
| Escopos de aplicação externa `service.*` | **BLOCKED (dependência)** | `external_app_installations.scopes` tem CHECK fechado na branch headless. |
| Cursor/`seq` denso por empresa para consumidores | **BLOCKED (dependência)** | É o sequenciador do diário `domain_events` da headless. |
| Receptor HTTP de webhook do ERP | **não construído** | Precisa de identidade de integração verificada (assinatura); o caso de uso `ApplyExternalUpdate` está pronto e testado para ser chamado por ele. |

## Critérios de aceite quando a headless for integrada (gate 4 → ativação)

1. Adicionar os 20 nomes públicos ao CHECK de `event_type` do diário e dos `webhook_subscriptions` e produzir o evento a partir de `context_events` (fonte única) — **sem** reescrever o serviço.
2. Entrega: `event_id` único como chave de deduplicação do consumidor; reprocessar o mesmo evento não muda nada; evento fora de ordem é descartado pela versão da entidade; revogar a credencial interrompe a entrega no próximo ciclo; empresa nunca vem do payload.
3. Testes: replay seguro, *out-of-order*, *dead letter* após o prazo, revogação de acesso no meio da entrega, empresa A nunca recebe evento da B.

Enquanto isso, o diário é consultável pela API (`…/timeline`) e pelo contexto do cliente.
