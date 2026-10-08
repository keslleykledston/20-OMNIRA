# AGENTS.md — Contrato de trabalho para agentes

## Antes de editar

Todo agente deve ler, nesta ordem:

1. `CONTEXT.md`
2. `docs/product/MVP.md`
3. o documento da feature/ticket
4. ADRs relacionados
5. arquivos que serão alterados

Não invente novos termos de domínio quando existir termo canônico.

## Multi-Agent Development

Este repositório pode ser alterado por Codex, Claude Code e Lovable. Git é a
fonte canônica do código e do estado de trabalho versionado.

Antes de trabalhar:

- inspecione status, branch e commits recentes;
- leia `docs/AI_HANDOFF.md` e `docs/AI_WORKFLOW.md`;
- leia os documentos de arquitetura relevantes;
- inspecione a implementação existente antes de propor substituições.

Não presuma acesso ao contexto de conversa de outro agente.

## Handoff

Ao concluir trabalho substancial, atualize `docs/AI_HANDOFF.md` com trabalho
concluído, estado atual, próximas tarefas, decisões e validações. Não inclua
segredos ou credenciais.

## Lovable

Lovable é principalmente agente de UI/UX/frontend. Prefira Codex ou Claude Code
para backend, APIs, banco, migrations, regras de negócio, testes,
infraestrutura, segurança, debugging e pequenas mudanças frontend. Revise código
gerado pela Lovable antes de aceitá-lo.

## Database Safety

Nunca altere schema de produção sem migration e revisão explícita. Nunca
execute SQL destrutivo sem solicitação explícita.

## Repository Safety

Preserve trabalho local desconhecido. Não force reset, limpe arquivos ou
reescreva histórico compartilhado sem autorização explícita.

## Router Shadow (Opcional — ROUTER_MODE=shadow)

Um roteador de custo/tier está em avaliação em `.agents/router/`. Em shadow
mode (default), ele recomenda um tier (TOOL < LOCAL_LLM < CHEAP_LLM <
FRONTIER_LLM < HUMAN) para tarefas elegíveis, sem forçar execução.

**Quando usar:**

Tarefas de engenharia (refactor, review, debug, docs) onde a escolha de
modelo/tier afeta custo/latência podem consultar:

```bash
.agents/router/route.sh --id <TASK-ID> --domains <csv> --desc "<descrição curta>"
```

Isso grava a recomendação em `logs/decisions.jsonl` para análise posterior.

**Quando NÃO usar:**

Nunca chamar router para: grep, git, lint, compiler output, tests diretos,
Docker checks, scripts determinísticos. Esses continuam TOOL direto.

**Ao executar a tarefa:**

Registre o resultado para fechar o loop de avaliação:

```bash
python3 .agents/router/log_outcome.py --task-id <TASK-ID> \
  --actual-tier <TIER> --actual-model <MODEL> --tokens N \
  --cost-usd C --latency-ms MS --test-result <pass|fail|n/a>
```

Ver `.agents/router/README.md` (arquitetura) e
`.agents/router/ROUTER.3-SHADOW-EVALUATION.md` (plano de avaliação).

## Princípios

- **Docker-first:** Todo componente executável deve possuir build Docker reproduzível. Veja `docs/deployment/DOCKER-FIRST.md`.
- Segurança de Tenant é requisito funcional, não detalhe de infraestrutura.
- Nunca confiar em `tenant_id` vindo do payload sem reconciliar com identidade e autorização.
- Não introduzir acesso cross-tenant fora do módulo de autorização do Hub.
- Preferir mudanças pequenas, verticais e testáveis.
- Não expandir escopo silenciosamente.
- Mudança irreversível ou cara de reverter exige ADR.
- Mudança visível ao usuário exige entrada em `.changes/`.
- Toda migration precisa de estratégia de rollback ou forward-fix documentada.
- Nunca gravar segredos, tokens, payloads sensíveis ou PII em fixtures públicas.
- Nenhum segredo em Dockerfile, imagem ou build arguments.

## Automation First

Antes de executar manualmente uma tarefa recorrente, o agente deve avaliar se ela deve virar:

- **tool/script:** validação ou transformação determinística;
- **skill:** workflow repetível composto por múltiplos passos/tools;
- **sub-agent:** trabalho paralelo/especializado com escopo isolado.

### Regra dos 2 usos

Após duas execuções semelhantes, avaliar automação **antes da terceira**.

Exceção: tarefa trivial de poucos segundos ou automação custaria mais que repetição.

### Prioridade de automação

Automatizar primeiro:

- isolation/RLS checks (determinísticos, rápidos, reutilizáveis em CI);
- migrations (validação de schema, rollback test, forward-fix);
- contract validation (OpenAPI/AsyncAPI: sintaxe, refs, versionamento);
- smoke tests (health endpoints, básico cross-tenant);
- DR restore (drill, verificação de RPO/RTO);
- release/changelog (entries, version bump, PRD alignment);
- observability checks (trace/log/metric presentes, sem PII);
- security regression (audit trail, grant validation, RLS enforce).

### Economia de contexto

- não recitar documentos canônicos; apontar para arquivos;
- decisões duráveis → ADR/CONTEXT;
- procedimentos recorrentes → skills;
- validações determinísticas → tools/scripts;
- outputs grandes → arquivos/artefatos, não chat.

### Restrições de segurança

Nenhuma automação pode:

- introduzir bypass de autorização/TenantContext;
- embutir ou armazenar segredos;
- executar ação destrutiva em produção sem flag/approval explícito;
- retornar PII ou credenciais desnecessariamente.

## Local AI — uso permitido

Existe uma camada opcional sobre o llama.cpp do host (`tools/ai/`) para
economizar contexto. Detalhes em `docs/architecture/LOCAL-AI.md`.

**Use para** comprimir entrada antes de gastar contexto do agente principal:
triagem de logs, triagem de testes, busca de contexto, pré-review de diff,
rascunho de changelog, classificação de checks a rodar.

**Prefira a tool local a despejar no chat:** logs extensos, saída completa de
testes, documentação inteira, diffs grandes.

**NUNCA** use a resposta do modelo local como evidência final para:

- isolamento de Tenant, RLS, RBAC/authz;
- segurança ou exposição de secrets;
- migration destrutiva;
- disaster recovery;
- aprovação final de código crítico.

Nesses casos o modelo local faz **apenas pré-análise**. O veredito final vem de
testes determinísticos + tools/checks + Claude/Codex.

**É opcional.** Com `LOCAL_AI_ENABLED=false` ou o servidor offline, as tools
caem para `grep` determinístico e saem com código 0. Nada em build, testes, CI
ou na aplicação pode depender do modelo local.

**Nunca** envie secrets, credenciais, dados pessoais reais, payloads de
clientes ou dados de produção. `ai_sanitize()` redige o que reconhece, mas a
responsabilidade de não enviar continua sendo de quem chama.

## Definição mínima de saída de um agente

Cada tarefa deve retornar:

- arquivos alterados;
- comportamento implementado;
- testes criados/executados;
- riscos ou dívida introduzida;
- documentação/ADR/changelog atualizados;
- itens explicitamente não feitos.

## Restrições

- Um sub-agent não deve editar uma área fora de seu write-scope sem devolver uma proposta ao orquestrador.
- Revisores não corrigem silenciosamente: registram findings; correções pertencem ao agente implementador.
- O agente de segurança pode bloquear merge por falha de isolamento, autenticação, autorização, exposição de segredo ou auditoria insuficiente.

## Protótipos de UI (Lovable e similares): referência, nunca a aplicação (ADR-0036)
- O OMNIRA atual (`web/`, `omnira-api`) é a fonte de verdade. Protótipos gerados só inspiram UX; **não** copie app shell, rotas, auth, estado, chamadas de API ou dados falsos deles, e **não** publique/redirecione domínios para eles.
- PRESERVE > EXTEND > REFACTOR LOCALLY > REPLACE. Mudança de frontend é aditiva, localizada e atrás de flag; o Hub convive com o workspace de tenant.
- O frontend nunca é autoridade de tenant: o servidor resolve hub → contrato → grant → `EffectiveTenantContext` e a RLS é a segunda barreira.
- Antes de uma fatia visual do Hub: ler `docs/ux/*` e `docs/architecture/HUB-VERIFICATION-STATUS.md`. Não use "pronto/produção/isolado/E2E passou" sem o gate executado; use IMPLEMENTED, NOT WIRED, UNIT/POSTGRES/HTTP/E2E VERIFIED, BLOCKED.
