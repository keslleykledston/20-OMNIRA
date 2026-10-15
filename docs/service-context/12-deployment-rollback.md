# 12 — Deploy e rollback

Plano de merge/implantação passo a passo: `15-merge-deploy-plan.md`.

Estado: **nada implantado, nada empurrado** (sem push, sem tag, sem merge). Branch `feat/omnira-service-context-operations` (commits sobre `main` @ `26e4f01`). Este documento é o plano para quando o dono mandar.

## Ordem

1. **Backup** (obrigatório antes de migrar): dump do banco de produção + conferir restore em ambiente descartável (`docs/ops` do projeto).
2. **Migrations 000150 → 000156** com `tools/migrate-sql.sh up` (idempotente, uma transação por arquivo; lacunas de numeração são aceitas). São **aditivas**: tabelas novas, 7 chaves `service.*` (concedidas a papéis de sistema), 1 `ALTER` de CHECK em `permission_domains` (000154), funções/políticas novas e, na 000155, o índice único global do prefixo de protocolo. **A 000155 tem pré-voo:** se duas empresas já compartilham o prefixo (banco onde a 000150–154 já emitiu protocolos com o prefixo padrão `AT`), ela para com mensagem clara; dê um prefixo a cada empresa antes (protocolos emitidos não são renomeados). Em banco que nunca recebeu 000150–154 (produção em `main` @ 137) não há o que colidir. A 000156 cria as funções que emitem o protocolo (`service_issue_protocol`, `service_prefix_candidates`, `service_suggest_prefix`) e troca as políticas do contador: o binário novo **depende dela** (sem a 000156 abrir sessão falha). Reverter: `down` 156→150 (round-trip verificado). Nenhuma toca `messages`, `conversations`, `tickets`. **Antes do binário.**
3. Binário **com todas as flags desligadas** (padrão). Comportamento idêntico ao de hoje; as migrations não mudam nada visível.
4. Ligar por etapas, observando `/metrics` (`omnira_service_*`) e logs (`service-context:`):

| Etapa | Flag | Efeito |
|---|---|---|
| a | `OMNIRA_SERVICE_CONTEXT_ENABLED=true` (api **e** worker) | rotas REST, painel "Protocolo e tarefas", nós de fluxo, ferramentas de IA, delegação do Hub |
| b | `OMNIRA_SERVICE_FINALIZE_HOOK_ENABLED=true` | `Finalize` (e o encerramento por fluxo) também fecha/cria a sessão, em savepoint; falha do módulo **nunca** impede finalizar |
| b2 | `OMNIRA_SERVICE_AUTO_OPEN_ON_CLAIM_ENABLED=true` (api) | quem **assume** a conversa (auto-atribuição do membro; agente do Hub no workspace) recebe o protocolo sem clicar. Em savepoint: assumir nunca depende do módulo; sem `service.create` não abre e não falha. Atribuição feita por gestor a outra pessoa **não** abre |
| c | `OMNIRA_SERVICE_EXTERNAL_ORDERS_ENABLED=true` | rotas que pedem algo ao ERP. **Sem adaptador real configurado, respondem 503 `not_configured`** (nunca sucesso falso). Não ligar até existir adaptador validado |

Conceder `service.*` a papéis personalizados é decisão do dono (os papéis de sistema já recebem: admin/supervisor tudo; agente sem `assign`/`request_external`).

## Pré-requisitos da tela do dono (decisões pendentes, não bloqueiam o código)

Papéis com `service.request_external`. Prefixo do protocolo: `GET/PUT /service-settings/protocol-prefix` (API; **ainda sem tela**): o administrador escolhe antes do primeiro protocolo, senão o sistema deriva do nome da empresa.

## Rollback

| Situação | Ação |
|---|---|
| Comportamento inesperado | desligar a flag (efeito imediato após reinício; dados ficam) |
| Binário | voltar o binário anterior: as migrations são aditivas e o binário antigo as ignora |
| Migration | **roll-forward preferido**. `down` existe (156 → 150, um por vez com `migrate-sql.sh down`) e **apaga os dados novos** (sessões, eventos, tarefas…); só em ambiente sem dados que importem. Round-trip 150–154 verificado |

## Colisão com o headless

`feat/headless-crm-dual-operation` usa migrations 137–141 (colide com o `000137` já em `main`) e ADR-0048. Este trabalho reservou **150–159** e **ADR-0049** justamente para não colidir. Quem mesclar o headless precisa renumerar as dele; ele também precisará estender os CHECKs de tipos/escopos de webhook para os eventos `service_*` (ver 06). **A entrega de webhooks a terceiros depende dessa branch.**

## Smoke (não executado — exige o dono)

1. Com a flag (a) ligada em staging: abrir uma conversa de teste, "Abrir atendimento com protocolo", classificar, criar tarefa, encerrar; conferir o protocolo e a linha do tempo.
2. Ligar (b): finalizar uma conversa de teste; conferir que a sessão fecha e que a conversa fecha mesmo se o módulo falhar (simular com permissão removida).
3. Hub: agente delegado com `service.read`/`create` abre sessão na fila do contrato e **não** vê a de outra fila.
4. Fluxo de teste com os cinco nós no simulador e depois num número de teste (não usar cliente real).
5. Telefone/ERP reais: **não** foram usados nesta entrega.

## Observabilidade

`omnira_service_sessions_opened_total{source}`, `…_closed_total{result}`, `…_reopened_total`, `omnira_service_cases_opened_total`, `omnira_service_tasks_created_total{origin}`, `omnira_service_external_requests_total{state}` (alerta sugerido: `outcome_unknown` parado), `omnira_service_external_updates_total{outcome}`. Logs sem PII; ids só em logs/traces.
