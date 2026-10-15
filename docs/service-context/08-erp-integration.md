# 08 — Integração ERP/CRM

## Princípio (ADR-06)

O **ERP é a fonte de verdade** das ordens de serviço. O OMNIRA guarda **referência, projeção mínima e histórico**; nunca cria OS própria. Nada no núcleo conhece um ERP: a única porta é `ports.ServiceOrderGateway` (+ `CorrelationLookup` opcional), implementada por adaptador por provedor.

## Modelo

- `external_records` — único por `(empresa, integração, tipo, id externo)`; `status_class` (open/in_progress/done/cancelled/unknown) + `external_status` (rótulo do ERP) + `sync_state` (synced/pending/stale/conflict/error); o **tempo do provedor** (`source_updated_at`) só avança (gatilho).
- `service_external_links` — o registro pertence a **exatamente uma** sessão, ocorrência ou tarefa; desvincular mantém o histórico; o mesmo registro pode estar em vários alvos sem duplicar.
- `external_record_requests` — a tentativa durável de pedir uma OS.

## Contratos (porta → caso de uso → rota)

| Operação do pedido | Caso de uso | Observação |
|---|---|---|
| consultar registro | `GetExternalRecord`, `RefreshExternalRecord` | refresh: ERP mudo ⇒ `stale`; ERP desconhece ⇒ `conflict`; nada é apagado |
| vincular existente | `LinkExternalRecord` | o ERP **confirma** que existe (nunca inventa); ERP mudo = `502`, não palpite |
| solicitar criação | `RequestServiceOrder` | só com flag; sem adaptador = `503 not_configured` **antes** de qualquer estado durável |
| consultar status | `RefreshExternalRecord` | |
| receber atualizações | `ApplyExternalUpdate` | idempotente, ordenada, nunca cria |
| reconciliar divergências | `ReconcileExternalRequest`, `ResolveExternalRequest` | ver abaixo |

## Resiliência: timeout não é falha

1. **A**, transação própria, *commit*: tentativa `in_flight` (+ evento). Um índice parcial único `(empresa, sessão, operação)` impede uma segunda tentativa `in_flight`/`outcome_unknown` para a mesma sessão.
2. **B**: chamada ao ERP com `CorrelationKey` estável, **sem trava no banco**, com prazo; o contexto não é cancelado se o cliente HTTP desistir (a escrita pode estar a caminho). Pânico no adaptador = `outcome_unknown`.
3. **C**, outra transação: `confirmed_success` (+ registro + vínculos) | `confirmed_failure` (**só** quando o ERP recusa definitivamente) | `outcome_unknown` (todo o resto).
4. `outcome_unknown` **bloqueia** nova solicitação da sessão (`409 request_blocked`) e a mesma chave **não** chama o ERP de novo.
5. **Reconciliar**: o ERP acha pela chave de correlação ⇒ confirmado e vinculado (sem escrever nada no ERP); não acha **e** a tentativa já tem idade suficiente (10 min) ⇒ falha confirmada, sessão liberada; ERP mudo ⇒ nada muda. ERP **sem** busca por correlação (como o K3G: ADR-0013, "no Idempotency-Key/externalReference/correlationId") ⇒ `needs_manual`: a pessoa confere no ERP e usa `resolve` (informa o id — o ERP é consultado para confirmar — ou o motivo de nada ter sido criado).

## Adaptadores reais

**Nenhum entregue.** Não há contrato de OS conhecido para o K3G/IXC além do chamado já tratado pelo módulo de tickets (ADR-0013); inventar um seria afirmar o que não foi provado. O que existe: a porta, o adaptador **fake** de contrato (`internal/servicectx/gatewaytest`, nunca instalado em execução) e testes de todos os cenários de falha (timeout após processar, descarte, recusa, pânico, ERP fora, sem busca por correlação). `OMNIRA_SERVICE_EXTERNAL_ORDERS_ENABLED` fica **desligada** até existir um adaptador com contrato validado.
