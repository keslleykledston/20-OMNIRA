# 11 — Evidência de testes

Tudo abaixo foi **executado** nesta sessão (2026-10-15) em Postgres 16 descartável (`omnira_test_svc`, recriado do zero antes de cada rodada; nunca `omnira_dev`), Go 1.25 em `golang:1.25` (`OMNIRA_INTEGRATION_TEST=1`), web com Vitest. Nenhum PASS sem rodar.

## Resultado por superfície

| Superfície | Comando | Resultado |
|---|---|---|
| Módulo `servicectx` (domínio + adapters, Postgres real) | `go test -count=1 -p 1 ./internal/servicectx/...` | ok (107 funções de teste; HTTP, RLS A/B, concorrência, ERP, eventos) |
| Hub (delegação) | `go test ./internal/hub/...` | `hub/adapters` ok (98 s), `hub/application`, `hub/distribution` ok |
| Flow Builder | `go test ./internal/flows/...` | ok (domínio, aplicação, adapters, templates) |
| IA + atendimento | `go test ./internal/intelligence/... ./internal/attendance/...` | ok |
| Suíte inteira, em série | `go test -count=1 -p 1 ./...` (HEAD de fase 6, 02:41) | **90 pacotes ok**, 4 pacotes falham (ver abaixo) |
| Linha de base (`main` @ `26e4f01`, mesmo comando, banco próprio) | idem | **88 ok**, **os mesmos 4 pacotes falham, com os mesmos testes** |
| Web | `vitest run` | 96 arquivos / **1012 testes** ok; `tsc --noEmit` ok; `vite build` ok |
| Migrations 000150–000155 | `down` (155→150) e `up` (150→155) em banco já migrado | ok, nas duas direções, depois da última alteração (000155: prefixo do protocolo). A 000155 também foi aplicada num banco descartável **populado**: com dois tenants no prefixo `AT` o pré-voo recusa com mensagem clara; com um prefixo para cada, aplica; `down`/`up` ok |
| `go build ./...`, `go vet` (servicectx, flows, intelligence, hub, apps) | — | ok |

### Os 4 pacotes que falham na suíte inteira (e na linha de base)

`hub/provisioning` (`TestPlatformOperator_GrantRevokeAndVisibility`: "operator rows = 8, want 1"), `outbox/adapters` (5 testes `FindUnpublished…`/`StoreUsesInjectedTransaction`), `tenancy/adapters` (`TestStandardSectorsMigration…`: roda `down` de migration contra o banco compartilhado) e `intelligence/adapters` (`TestQueriesStayFastOnABusyConversation`, orçamento de tempo). São testes que **assumem banco virgem / contagens globais**; falham no `main` sem as minhas mudanças e **passam isolados em banco novo** (verificado para cada um). Não foram alterados nem "contornados". Diga-se também: a ordem entre pacotes importa (`attendance`, `intelligence` e `hub/provisioning` falham se rodarem **depois** de pacotes que deixam linhas; na ordem do gate passam).

## Gates da especificação

| Gate | Evidência |
|---|---|
| 1. Atendimento informativo completo (protocolo → mensagens por intervalo → classificação → encerramento) | `sessions_integration_test`, `episodes_integration_test`, `finalize_hook_integration_test`, `http_integration_test` |
| 2. Concorrência/idempotência | protocolo único e denso sob concorrência; N aberturas simultâneas = 1 sessão; mesma chave em paralelo (sessão, tarefa, pedido ao ERP); retry depois do fechamento devolve a mesma resposta |
| 3. Ocorrências e tarefas (5 cenários) | `cases_tasks_integration_test` (tarefa sem ocorrência; ocorrência N:M com sessões; estados independentes; concluir tarefa não resolve ocorrência; cancelar exige motivo) |
| 4. ERP | timeout ⇒ `outcome_unknown` bloqueia novo pedido; reconciliação por correlação; ERP sem busca ⇒ manual; atualização fora de ordem; resposta de tipo/id/correlação errada nunca confiada; chave de pedido vs chave de vínculo separadas no SQL |
| 5. RLS/segurança | A/B por tabela, membership revogada, delegado sem concessão/somente leitura/revogada/contrato inativo/outra instância/tarefa só de contato; SQL cru recusado (`42501`) |
| 6. Fluxo/IA | cadeia dos 5 nós em Postgres real; idempotência por passo; IA só propõe; ferramentas que não existem recusadas |
| 7. Observabilidade | contadores com rótulos fechados; sem identificadores em `/metrics` |

## Mutações (a defesa removida ⇒ o teste falha)

Verificadas removendo a defesa e vendo o teste falhar (depois restaurada): trava da conversa; contador transacional; savepoint do hook; ator forjado em `context_events`; leitura por chave no banco; motivo de reabertura no banco; recibo de passo de fluxo; chaves `link_external` vs `request_external` no SQL; tipo da resposta do ERP; snapshot sem horário; lease da reconciliação; correlação estrangeira; tarefa/evento só de contato visível ao delegado (política de tarefas e a de eventos separadamente); chave de cache por Hub (web); fachada de fluxo aceita pessoa; run↔conversa; chave de caso aleatória; assinatura `flow`; recibo de classificar/encerrar e o tombstone de "nada a fechar"; proposta de IA como leitura; origem `ai` nunca gravada; ETag no spec.
Mutações **não** provadas (dito com franqueza): o savepoint do `FlowEffects` (uma recusa da aplicação não aborta a transação; só um erro de banco exigiria o savepoint — defesa em profundidade); o deadline total de 15 s das transações independentes (não há teste que segure uma transação por 15 s; o `lock_timeout` tem teste).

## Revisões independentes (Codex, somente leitura, sem `go` no ambiente dele)

| Rodada | Alvo | Achados | Tratamento |
|---|---|---|---|
| 0 | docs da fase 0 | 4 HIGH / 6 MEDIUM | corrigidos (outbox descartado, episódios, recibos, estados no banco, ator, leitura por chave) |
| 1–2 | sessões + timeline | 4 HIGH / 5 MEDIUM | corrigidos |
| 3 | ocorrências/tarefas | 5 HIGH / 4 MEDIUM / 2 LOW | corrigidos |
| 4 | ERP | 5 HIGH / 3 MEDIUM | corrigidos |
| 5 | Hub + front | 2 HIGH / 3 MEDIUM / 1 LOW | corrigidos |
| 6 | fluxos/IA/métricas | 2 HIGH / 1 MEDIUM / 1 LOW | 1 HIGH + MEDIUM + LOW corrigidos; **1 HIGH registrado como risco herdado (R-AI-1)** |
| 7 (final) | correções da 6 + docs | 1 HIGH / 3 LOW | HIGH (fechar sem sessão ativa) e LOW corrigidos; round-trip agora registrado aqui |

Saídas completas na pasta de trabalho da sessão (`svc-codex-p*-out.md`). **Não** se declara "zero achados": cada rodada achou algo novo; a última rodada **não foi repetida** depois da última correção (tombstone), que foi verificada por teste e mutação, não por Codex.

## Não executado

Playwright/e2e; ESLint do web (binário ausente neste ambiente); carga/desempenho além dos testes de concorrência; smoke com telefone, ERP ou staging reais; restore de backup com estas migrations.
