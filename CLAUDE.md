# Instruções para Claude Code — OMNIRA

Leia `START-HERE.md` antes de qualquer alteração.

## Modo de trabalho

Você é o agente implementador principal. Não reescreva a arquitetura por preferência pessoal.

### Antes de editar
- leia a spec do release/ticket;
- leia ADRs relacionados;
- inspecione o código existente;
- identifique o menor slice vertical necessário.

### Durante a implementação
- preserve boundaries de módulo;
- não transforme o projeto em microserviços;
- não introduza Kubernetes/Temporal/Kafka sem ADR aprovado;
- toda query tenant-owned precisa de TenantContext;
- mantenha OpenAPI/AsyncAPI alinhados;
- adicione telemetria a operações críticas;
- mantenha migrations reproduzíveis.

### Testes
Sempre que tocar tenancy:
- crie Tenant A/B;
- tente acesso cruzado;
- teste leitura e escrita;
- teste membership revogada;
- execute testes contra PostgreSQL real.

### Frontend
Siga `docs/architecture/FRONTEND-UX.md`.
A UI deve ser limpa, clara e responsiva. Não copie interfaces da Apple literalmente.

### Deploy
Siga `docs/architecture/DEPLOYMENT-NGINX.md`.
Nunca exponha `/internal/*` no Nginx público.

## Commits/PRs

Prefira commits pequenos por ticket.
O resumo da PR deve citar ticket/release e informar:
- comportamento;
- segurança;
- testes;
- migration;
- observabilidade.

## Proibido sem aprovação
- remover RLS/enforcement de tenancy;
- usar `tenant_id` do payload como autorização;
- colocar token/secret em fila;
- usar Valkey como única fonte de dado crítico;
- mudar backend principal de Go;
- mudar o domínio público;
- alterar o escopo do MVP silenciosamente.

## Protótipos de UI (Lovable e similares): referência, nunca a aplicação (ADR-0036)
- O OMNIRA atual (`web/`, `omnira-api`) é a fonte de verdade. Protótipos gerados só inspiram UX; **não** copie app shell, rotas, auth, estado, chamadas de API ou dados falsos deles, e **não** publique/redirecione domínios para eles.
- PRESERVE > EXTEND > REFACTOR LOCALLY > REPLACE. Mudança de frontend é aditiva, localizada e atrás de flag; o Hub convive com o workspace de tenant.
- O frontend nunca é autoridade de tenant: o servidor resolve hub → contrato → grant → `EffectiveTenantContext` e a RLS é a segunda barreira.
- Antes de uma fatia visual do Hub: ler `docs/ux/*` e `docs/architecture/HUB-VERIFICATION-STATUS.md`. Não use "pronto/produção/isolado/E2E passou" sem o gate executado; use IMPLEMENTED, NOT WIRED, UNIT/POSTGRES/HTTP/E2E VERIFIED, BLOCKED.
