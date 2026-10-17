# 13 — Relatório final

## Implementation Summary

Branch `feat/omnira-service-context-operations` (20 commits sobre `main` @ `26e4f01`; 130 arquivos; **sem push, sem tag, sem merge**). Worktree `/data/cafegpt/tmp/wt-svcctx`.

| Fase | Entrega | Onde |
|---|---|---|
| 0 | Auditoria, arquitetura, matriz de impacto, plano, ADR-0049 | `docs/service-context/00–03`, `docs/adr/0049-…` |
| 1–2 | Atendimento (sessão) por conversa com protocolo `<PREFIXO>-n` por empresa (início 1001); mensagens por **episódios** (sem coluna em `messages`); catálogos (categorias, códigos de resultado); estados no banco; reabertura com motivo; linha do tempo imutável (`context_events`); visão de contexto do cliente; hook opcional no `Finalize` (savepoint: finalizar nunca depende do módulo) | `migrations/000150–151`, `internal/servicectx/*`, `contracts/openapi/service-context-v1.yaml` |
| 3 | Ocorrências (`OC-n`), N:M com sessões, tarefas (com ou sem ocorrência), chaves por tipo de mudança no banco, concorrência otimista (`ETag`/`If-Match`) | `migrations/000152` |
| 4 | Registros externos ERP/CRM, vínculos, **pedido durável** de OS (tentativa gravada antes da chamada; timeout = desconhecido, nunca falha; reconciliação; resolução manual), atualização de entrada idempotente e ordenada, porta `ServiceOrderGateway` + fake; contrato AsyncAPI + JSON Schema do envelope | `migrations/000153`, `docs/service-context/06,08` |
| 5 | Delegação do Hub (domínio `service`, escopo de fila, uma instância por vez, `request_external` nunca delegável); painel "Protocolo e tarefas" no Workspace e no Hub | `migrations/000154`, `web/src/components/service` |
| 6 | 5 nós do Flow Builder; 4 ferramentas de IA (lê / propõe, pessoa aprova); métricas `omnira_service_*` | `internal/flows`, `internal/intelligence`, `internal/servicectx/adapters/{flow_effects,ai_bridge,metrics}.go` |
| 7 | Documentação 09–13, matriz do Hub, riscos residuais | `docs/service-context/` |

Flags (todas **desligadas** por padrão): `OMNIRA_SERVICE_CONTEXT_ENABLED`, `OMNIRA_SERVICE_FINALIZE_HOOK_ENABLED`, `OMNIRA_SERVICE_EXTERNAL_ORDERS_ENABLED`, `OMNIRA_SERVICE_AUTO_OPEN_ON_CLAIM_ENABLED`.

## Validation Summary

Ver `11-test-evidence.md`. Em resumo: módulo, Hub, fluxos, IA, atendimento e web **passam**; a suíte inteira em série tem **90 pacotes ok e 4 falhando — exatamente os mesmos 4 (mesmos testes) que falham no `main` sem estas mudanças** (dependem de banco virgem) e que passam isolados. Migrations 150–156 com round-trip. 7 rodadas de revisão independente (Codex) com achados corrigidos e mutações verificadas.

## Deployment Readiness

Ver `12-deployment-rollback.md`: migrations aditivas antes do binário, flags desligadas, ligar por etapas, rollback por flag/binário (migration `down` apaga dados novos). **Nada foi implantado.** Colisão de numeração com `feat/headless-crm-dual-operation` documentada (este trabalho usa 150–159).

## O que NÃO está pronto (honesto) — atualizado em 2026-10-17

Entregue desde a primeira versão deste relatório (ver `11`, adendo): telas de ocorrências, ordens de serviço no ERP (lista) e histórico; resultado escolhido no Finalizar; tela do prefixo; protocolo visível na conversa; aviso ao cliente (chave por empresa); status do chamado ERP pelo Hub (chave por empresa); e2e com o binário real; restauração de backup; carga leve; contrato do adaptador de ERP; R-7 fechado.

Ainda aberto:

- **Adaptador real de ERP**: o K3G não oferece o contrato (idempotência/correlação, OS). `docs/service-context/16` diz exatamente o que o provedor precisa fornecer e o teste de contrato (`gatewaytest.Check`) é o aceite. Pedir OS responde `503 not_configured` até lá; flag (c) desligada. `BLOCKED` por terceiro.
- **Entrega de webhooks a terceiros / aplicação externa**: a headless foi **mesclada em `main`** por ordem do dono (migrations 159–163) e o **gate 4 está construído e testado** (migration 164: 20 nomes públicos, escopo `service.read`, API de eventos e webhook assinado; ver `06`). **Nenhum dos dois está implantado em produção** nem tem flag ligada.
- **Pedido de OS pela tela**: a tela lista os pedidos; o botão de pedir não existe porque a flag (c) está desligada e a chave `service.request_external` não é concedida a papel algum (decisão do dono).
- **R-AI-1** (Codex HIGH herdado do ADR-0017): a rota de ferramentas de IA aceita `proposed_by` do cliente. Decisão do dono; recomendação: proveniência do lado do servidor.
- **Riscos aceitos** (07): R-8 (tentativas de status partilhadas entre atendentes do mesmo escopo), R-9 (aviso que falha depois do commit só vai ao log).
- **Não executado**: smoke com telefone real (aviso ao cliente, anexos), canal oficial Meta, staging, Playwright de navegador. ESLint do web **não existe** no projeto.
- **Decisões do dono pendentes**: papéis personalizados com `service.*` e `service.request_external`; aprovação do conjunto para staging.

## Final Status

**READY_FOR_REVIEW.**

Operando em produção, sob supervisão do dono, na K3G_Solutions e nas empresas internas (flags a, b, b2 ligadas; aviso só na K3G_Solutions). Não é `READY_FOR_STAGING`: falta aprovação humana do conjunto, decidir R-AI-1 e o smoke com telefone real. Não é `READY_FOR_PILOT` para o ERP: não há adaptador nem contrato do provedor. Nenhuma alegação de `PRODUCTION_READY`.
