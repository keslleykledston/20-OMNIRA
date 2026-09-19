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
