# 13 — Relatório final

## Implementation Summary

Branch `feat/omnira-service-context-operations` (11 commits sobre `main` @ `26e4f01`; 115 arquivos; **sem push, sem tag, sem merge**). Worktree `/data/cafegpt/tmp/wt-svcctx`.

| Fase | Entrega | Onde |
|---|---|---|
| 0 | Auditoria, arquitetura, matriz de impacto, plano, ADR-0049 | `docs/service-context/00–03`, `docs/adr/0049-…` |
| 1–2 | Atendimento (sessão) por conversa com protocolo `AT-n` por empresa; mensagens por **episódios** (sem coluna em `messages`); catálogos (categorias, códigos de resultado); estados no banco; reabertura com motivo; linha do tempo imutável (`context_events`); visão de contexto do cliente; hook opcional no `Finalize` (savepoint: finalizar nunca depende do módulo) | `migrations/000150–151`, `internal/servicectx/*`, `contracts/openapi/service-context-v1.yaml` |
| 3 | Ocorrências (`OC-n`), N:M com sessões, tarefas (com ou sem ocorrência), chaves por tipo de mudança no banco, concorrência otimista (`ETag`/`If-Match`) | `migrations/000152` |
| 4 | Registros externos ERP/CRM, vínculos, **pedido durável** de OS (tentativa gravada antes da chamada; timeout = desconhecido, nunca falha; reconciliação; resolução manual), atualização de entrada idempotente e ordenada, porta `ServiceOrderGateway` + fake; contrato AsyncAPI + JSON Schema do envelope | `migrations/000153`, `docs/service-context/06,08` |
| 5 | Delegação do Hub (domínio `service`, escopo de fila, uma instância por vez, `request_external` nunca delegável); painel "Protocolo e tarefas" no Workspace e no Hub | `migrations/000154`, `web/src/components/service` |
| 6 | 5 nós do Flow Builder; 4 ferramentas de IA (lê / propõe, pessoa aprova); métricas `omnira_service_*` | `internal/flows`, `internal/intelligence`, `internal/servicectx/adapters/{flow_effects,ai_bridge,metrics}.go` |
| 7 | Documentação 09–13, matriz do Hub, riscos residuais | `docs/service-context/` |

Flags (todas **desligadas** por padrão): `OMNIRA_SERVICE_CONTEXT_ENABLED`, `OMNIRA_SERVICE_FINALIZE_HOOK_ENABLED`, `OMNIRA_SERVICE_EXTERNAL_ORDERS_ENABLED`.

## Validation Summary

Ver `11-test-evidence.md`. Em resumo: módulo, Hub, fluxos, IA, atendimento e web **passam**; a suíte inteira em série tem **90 pacotes ok e 4 falhando — exatamente os mesmos 4 (mesmos testes) que falham no `main` sem estas mudanças** (dependem de banco virgem) e que passam isolados. Migrations 150–154 com round-trip. 7 rodadas de revisão independente (Codex) com achados corrigidos e mutações verificadas.

## Deployment Readiness

Ver `12-deployment-rollback.md`: migrations aditivas antes do binário, flags desligadas, ligar por etapas, rollback por flag/binário (migration `down` apaga dados novos). **Nada foi implantado.** Colisão de numeração com `feat/headless-crm-dual-operation` documentada (este trabalho usa 150–159).

## O que NÃO está pronto (honesto)

- **Adaptador real de ERP**: não existe contrato validado do provedor (o K3G não tem idempotência/correlação). Pedir OS ao ERP responde `503 not_configured` até haver adaptador; o fake nunca é instalado no runtime. `BLOCKED`.
- **Entrega de webhooks a terceiros / autenticação de aplicação externa**: depende da branch headless (ADR-0048). O módulo guarda os eventos (`context_events`) e documenta o envelope; **não emite ao outbox** (decisão registrada). `BLOCKED` por dependência.
- Telas de ocorrências, OS externas e linha do tempo no Workspace; escolha do resultado no `FinalizeDialog` (a API existe).
- **Risco R-AI-1** (Codex HIGH): a rota de ferramentas de IA aceita `proposed_by` do cliente; comportamento herdado do ADR-0017, decisão do dono (ver 07).
- Não executado: smoke com telefone/ERP/staging, e2e Playwright, ESLint do web, carga, restore de backup.
- Decisões do dono pendentes (não bloqueiam): prefixo/número inicial do protocolo, janela de reabertura (72 h), papéis com `service.request_external`, abertura automática ao assumir.

## Final Status

**READY_FOR_REVIEW.**

Não é `READY_FOR_STAGING`: falta aprovação humana do conjunto, decidir R-AI-1, e o smoke em staging. Não é `READY_FOR_PILOT`: não há evidência de gate de segurança + integração em ambiente real (nem adaptador de ERP), e a última correção de segurança (tombstone) não passou por nova rodada do Codex. Nenhuma alegação de `PRODUCTION_READY`.
