# 16 — O que o OMNIRA precisa de um ERP para criar Ordens de Serviço (contrato do provedor)

Estado: **especificação para o fornecedor do ERP (K3G) e checklist de aceite do adaptador.** Nenhum adaptador real existe (ver `08`): sem contrato validado do provedor, `OMNIRA_SERVICE_EXTERNAL_ORDERS_ENABLED` fica **desligada** e pedir uma OS responde `503 not_configured` antes de qualquer estado durável.

Regra do dono (2026-10-16): **o ERP só recebe registro quando há tarefa/ocorrência ou Ordem de Serviço.** O protocolo sozinho nunca cria nada no ERP.

## 1. O que precisamos do provedor

| # | Necessidade | Por quê | Hoje no K3G |
|---|---|---|---|
| 1 | **Criar OS** (`POST`), devolvendo o registro criado com id estável | `CreateServiceOrder` | existe para **chamado** (`/api/support/tickets`), não confirmado para OS |
| 2 | **Ler OS por id** (`GET`) com status e data de atualização do provedor | `Get`, `Refresh` | idem (chamado) |
| 3 | **Chave de idempotência ou de correlação** aceita na criação (`Idempotency-Key`, `externalReference` ou `correlationId`) e **devolvida** na leitura | repetir uma chamada de que não se soube o resultado **não pode** duplicar a OS | **não existe** (ADR-0013) |
| 4 | **Busca por chave de correlação** (`GET …?correlationId=`) | reconciliar `outcome_unknown` sem intervenção humana | **não existe**: a reconciliação é manual (`resolve`) |
| 5 | **Erros classificados**: 4xx de validação/permissão/cliente inexistente = recusa definitiva; 5xx, timeout e queda = desconhecido | só uma recusa **definitiva** libera a sessão para tentar de novo | a confirmar |
| 6 | **Vocabulário de status** documentado (códigos e significado) | mapear para `open / in_progress / done / cancelled` | códigos 1–6 só para chamados |
| 7 | **Atualizações**: webhook assinado ou consulta incremental por `updatedAfter` | `ApplyExternalUpdate` (idempotente, ordenada, nunca cria) | a confirmar |
| 8 | **Sandbox** separado da produção, com credencial própria | o teste de contrato cria OS de verdade | a confirmar |
| 9 | **Limites** (taxa, tamanho de título/descrição) | validar antes de chamar | a confirmar |

Sem os itens 3 e 4 o adaptador pode existir, mas o módulo trata toda falha ambígua como `outcome_unknown` e **bloqueia** novos pedidos da sessão até a pessoa conferir no ERP e resolver à mão. Isso é seguro, só não é automático.

## 2. O adaptador (do nosso lado)

Implementa `ports.ServiceOrderGateway` (`System`, `Get`, `CreateServiceOrder`) e, se o item 4 existir, `ports.CorrelationLookup`. Regras que o código do módulo assume (e que o teste de contrato confere):

- **nunca repete uma escrita** (uma chamada HTTP por `CreateServiceOrder`);
- erro é sempre `*ports.GatewayError` com `Kind` (`rejected`, `not_found`, `unknown`, `not_supported`) e `Class` em token fechado (nunca texto do provedor);
- contexto encerrado ⇒ `unknown` (nunca `rejected`, nunca sucesso);
- as credenciais são resolvidas pelo par **(empresa, integração)**, nunca só pela integração;
- o instantâneo devolvido passa em `Snapshot.Clean()` e traz o tipo `service_order`.

## 3. Teste de contrato (aceite do adaptador)

`internal/servicectx/gatewaytest.Check(gateway, customerRef)` devolve a lista de problemas (vazia = conforme) e `RunContract(t, gateway, customerRef)` falha o teste para cada um. Roda contra o **sandbox** do provedor. Confere: nome do sistema em token; OS criada volta como instantâneo utilizável do tipo certo, sem chave de correlação de **outro** pedido; leitura devolve o mesmo registro; id inexistente é `not_found` classificado; busca por correlação (se houver) acha exatamente a OS criada e responde `not_found` para chave desconhecida; contexto já encerrado é `unknown`. O próprio teste de contrato tem testes que provam que ele **reprova** cada defeito (adaptador que classifica errado, devolve OS sem id, etc.).

## 4. Para ligar (só com ordem do dono, depois de tudo acima)

1. Adaptador passa em `RunContract` no sandbox; revisão do Codex sobre o adaptador.
2. Integração cadastrada na empresa de teste; credencial cifrada como as demais (ADR-0009).
3. `OMNIRA_SERVICE_EXTERNAL_ORDERS_ENABLED=true` (api e worker recriados); papéis que recebem `service.request_external` + `service.link_external` definidos pelo dono (**nunca delegáveis ao Hub, nunca executáveis pela IA**).
4. Smoke com número de teste: pedir OS, perder a resposta de propósito (derrubar o sandbox), reconciliar.
5. Rollback: flag `false` + recriar api e worker. As OS já criadas no ERP e os registros locais permanecem.
