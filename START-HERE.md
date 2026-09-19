# OMNIRA — START HERE

Este pacote é o contrato de direção para o agente responsável por construir o OMNIRA.

## Objetivo

Construir o OMNIRA por **entregas pequenas, funcionais e demonstráveis**, preservando desde o início:

- isolamento Multi-Tenant;
- caminho para Hub BPO multicontas;
- mensageria e Tool Runtime;
- observabilidade;
- disaster recovery;
- APIs versionadas;
- frontend intuitivo;
- deploy reproduzível.

## Ordem obrigatória de leitura

Antes de gerar código, leia nesta ordem:

1. `CONTEXT.md`
2. `docs/product/PRD.md`
3. `docs/product/MVP.md`
4. `docs/delivery/DELIVERY-SLICES.md`
5. `docs/delivery/R0.1-TENANT-FOUNDATION.md`
6. `docs/architecture/ARCHITECTURE.md`
7. `docs/architecture/TENANCY-SECURITY.md`
8. `docs/architecture/PLATFORM-BASELINE.md`
9. `docs/architecture/MESSAGING-JOBS-TOOLS.md`
10. `docs/architecture/API-GOVERNANCE.md`
11. `docs/architecture/OBSERVABILITY.md`
12. `docs/architecture/FRONTEND-UX.md`
13. `docs/architecture/DEPLOYMENT-NGINX.md`
14. `AGENTS.md`
15. `CLAUDE.md` ou `CODEX.md`

Depois, consulte ADRs relevantes antes de implementar.

## Decisões congeladas

- **Execução: Docker-first obrigatório.** Todo componente executável deve possuir forma reproduzível em Docker. Veja `docs/deployment/DOCKER-FIRST.md`.
- Backend principal: **Go**.
- Frontend: **Next.js + TypeScript**.
- Banco: PostgreSQL.
- Mensageria/jobs: NATS + JetStream.
- Cache/presence: Valkey, apenas quando necessário.
- Telemetria: OpenTelemetry.
- Estratégia inicial: monólito modular + workers.
- Tenant primeiro; Hub BPO entra depois.
- Shared Strong Isolation no SaaS padrão.
- Dedicated Database disponível como tier Enterprise/BPO.
- PostgreSQL é source of truth.
- REST/OpenAPI primeiro.
- AsyncAPI para eventos.
- Nginx expõe a aplicação em:
  `https://omnira.devops.k3gsolutions.com.br`
- Orquestração: Docker Compose como padrão inicial (Kubernetes apenas com ADR aprovado).

## Regra de execução

Não tente construir o MVP inteiro de uma vez.

Execute release por release:

```text
R0.1 Tenant Foundation
R0.2 Atendimento Single-Tenant
R0.2.1 WhatsApp
R0.3 Tool Runtime + IXC
R0.4 Hub BPO
R0.5 Supervisor
R0.6 Automação mínima
```

Não avance para a próxima release com BLOCKER de segurança/isolamento aberto.

## Regra para decisões não especificadas

Se uma decisão:
- for reversível e local: escolha a solução mais simples e documente.
- afetar tenancy, segurança, contratos, dados ou arquitetura: crie/proponha ADR antes.
- exigir produto/comercial: registre em `docs/decisions/OPEN-QUESTIONS.md`; não invente requisito.

## Saída obrigatória a cada ciclo

Ao finalizar um ticket/release, reporte:

1. o que foi implementado;
2. arquivos alterados;
3. migrations;
4. endpoints/eventos novos;
5. testes executados;
6. riscos/dívida;
7. documentação/changelog atualizados;
8. próximo ticket recomendado.
