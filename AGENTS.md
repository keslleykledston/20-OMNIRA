# AGENTS.md — Contrato de trabalho para agentes

## Antes de editar

Todo agente deve ler, nesta ordem:

1. `CONTEXT.md`
2. `docs/product/MVP.md`
3. o documento da feature/ticket
4. ADRs relacionados
5. arquivos que serão alterados

Não invente novos termos de domínio quando existir termo canônico.

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
