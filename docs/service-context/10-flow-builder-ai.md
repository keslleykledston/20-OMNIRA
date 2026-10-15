# 10 — Flow Builder, IA e métricas (fase 6)

## Nós do Flow Builder

Cinco nós, finos, sobre uma capacidade opcional dos efeitos (`ports.ServiceFlow`). O fluxo age como **sistema**, assina tudo como `flow`, só toca a conversa que a execução atende (`in.Facts.ID`; a empresa é a do contexto) e cada efeito é idempotente por **(run, passo)**.

| Nó | Efeito | Portas | Observações |
|---|---|---|---|
| `start_service_session` | abre a sessão (ou devolve a que existe) | `next`, `error` (opcional) | `variable` recebe o protocolo (`<PREFIXO>-n`) |
| `classify_service_session` | categoria por **código** | idem | código desconhecido/inativo → erro |
| `create_service_task` | tarefa pendente, origem `flow`, **sem responsável** | idem | título/descrição aceitam `{{variável}}` |
| `open_service_case` | ocorrência a partir da sessão | idem | `variable` recebe OC-n |
| `close_service_session` | encerra **só o protocolo** | idem | a conversa continua aberta (isso é `close_conversation`); `abandoned` não é escolhível |

- Cada chamada roda sob **savepoint**: uma recusa do módulo desfaz só aquela chamada e o nó segue a porta `error` numa transação ainda utilizável; sem porta ligada o run falha e a conversa volta aos humanos (padrão dos outros nós).
- Idempotência por **(run, passo)** em todos os cinco: sessão pela chave `flow:<run>:<seq>`; tarefa por recibo + índice único `(flow_run_id, flow_seq)`; caso pela chave do passo; **classificar e encerrar** deixam um recibo `flow.step` (000153) apontando para a sessão em que agiram, com o hash do conteúdo: o passo repetido não faz nada, **mesmo que outra sessão esteja ativa depois**; o mesmo passo com outro conteúdo é recusado. Fechar sem sessão ativa devolve `closed=false` sem erro e **também é final**: o recibo `flow.step` fica sem sessão (tombstone), de modo que a repetição do passo não fecha uma sessão aberta depois.
- `flowCtx` só aceita a **sessão de sistema** E um **run que existe nesta empresa e atende exatamente aquela conversa** (`FlowRunAttends`): a fachada não consegue saber *quem* chama, mas se recusa a assinar `flow` para um run numa conversa que não é a dele (outra conversa da mesma empresa, run inexistente ou de outra empresa). Uma pessoa que a chame é recusada. O marcador de ator `flow` só é lido em sessão de sistema e só é posto depois dessas verificações. Os chamadores internos legítimos (`ApplyExternalUpdate`, `FinalizeHook`) não usam a fachada.
- Módulo desligado (`OMNIRA_SERVICE_CONTEXT_ENABLED=false`): os efeitos não existem; cada nó toma `error` (ou falha o run). Simulador: registra o que faria e inventa `AT-SIM`/`OC-SIM`; nunca chega ao módulo.
- **Não** são nós de fluxo: pedir OS ao ERP e vincular registro externo (exigem `service.request_external`/`link_external` e adaptador real, que não existe).

## IA (tool gateway, ADR-0017)

| Ferramenta | Risco | Permissão | Quando a IA pede |
|---|---|---|---|
| `service.current_session`, `service.open_tasks` | leitura | `service.read` | executa |
| `service.propose_task` | `low_write` | `service.create` | fica `pending_approval` |
| `service.propose_classification` | `low_write` | `service.update` | fica `pending_approval` |

- A pessoa que **aprova** executa com as **próprias** permissões; a tarefa guarda `origin='ai'` e o histórico é assinado por quem aprovou. A conversa vem do tópico no servidor (nenhum argumento aponta outra).
- Argumentos estritos (sem `tenant_id`, responsável, etc.; título de uma linha, sem credencial; prioridade fechada).
- **Não existem** como ferramenta: fechar sessão, ocorrências, vincular registro, `service.request_external` (nunca executável pela IA). Teste de domínio fixa o conjunto.
- Só entra no catálogo com `OMNIRA_SERVICE_CONTEXT_ENABLED`; sem o módulo, nenhuma ferramenta `service.*`.

## Métricas

`omnira_service_*` no registro `opsmetrics` (api e worker já o expõem em `/metrics`): sessões abertas (origem) / encerradas (resultado canônico) / reabertas, ocorrências, mudanças de status de ocorrência e de tarefa, tarefas criadas (origem), vínculos, pedidos ao ERP (estado), atualizações de entrada (resultado). Todo valor de rótulo vem de um **conjunto fechado** em código; fora dele o contador descarta e conta. Nenhum id de empresa, pessoa, conversa ou protocolo chega a `/metrics` (teste). Logs/traces é que carregam ids.

## Evidência

`internal/flows/{domain,application}/service_nodes*_test.go` (validação, simulador, porta de erro, Postgres real: cadeia completa, idempotência por passo, recusa + transação utilizável, conversa fechada, pessoa recusada), `internal/intelligence/{domain,adapters}/service_tools*_test.go`, `internal/servicectx/adapters/metrics_test.go`, `web/src/__tests__/ServiceNodes.test.tsx`. Mutações verificadas: fachada aceita pessoa; chave de caso aleatória; eventos sem assinatura `flow`; proposta como leitura; origem nunca `ai`.
