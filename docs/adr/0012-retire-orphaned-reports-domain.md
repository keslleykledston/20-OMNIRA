# ADR-0012: Retire orphaned legacy `internal/reports` package

## Status
Accepted

## Contexto

`internal/reports` foi auditado durante o PRODUCT.3 (Analytics Reality /
Prioritization Gate) com o mesmo padrão de ceticismo já aplicado a
`internal/bpo` (ADR-0011). O PRODUCT.4-A reconfirmou integralmente essa
evidência antes desta decisão:

- **Zero consumidores ativos**: nenhum import de `internal/reports` fora do
  próprio pacote em todo o repositório.
- **Zero wiring**: não registrado em `internal/platform/httpserver/server.go`
  (a única ocorrência da palavra "reports" ali é um comentário não
  relacionado), não instanciado em nenhuma composition root.
- **Zero schema/migração**: nenhum arquivo em `migrations/` cria ou
  referencia qualquer tabela do domínio de relatórios.
- **Zero acesso a banco de dados/RLS**: `ReportService`
  (`internal/reports/application/report_service.go`) não tem repositório
  algum — comportamento fabricado em memória:
  - `GenerateReport` sempre produz 10 linhas hardcoded
    (`"ticket-0".."ticket-9"`, `status:"open"`, `priority:"medium"`),
    independentemente de qualquer dado real de tenant.
  - `ExportHandlers.ExportReportJSON`/`CSV`
    (`internal/reports/adapters/export_handlers.go`) constroem um
    `// Placeholder report` hardcoded antes mesmo de chamar o exportador —
    nunca consultam dados reais.
  - `ListTemplates` sempre retorna slice vazio; `GetTemplate` sempre
    retorna erro `"not found"`.
- **Zero modelo de permissão**: nenhuma checagem de autorização em
  qualquer parte do pacote.
- **Testes apenas de compilação/unidade**: `TestExportHandlersCompile`
  espelha o stub de compilação do `bpo`; os demais testes só validam os
  próprios dados fabricados em memória — nenhum teste de integração real
  contra Postgres, nenhum teste de RLS, nenhum teste HTTP contra servidor
  real.
- **Frontend mock desalinhado**: `/reports` (contido, `isDevSurface()`)
  oferece 4 templates fake — "Conformidade SLA" (sem domínio SLA
  existente), "Métricas de Tickets" (fonte real existe desde o PRODUCT.2,
  mas nenhuma capacidade de relatório existe), "Performance da Equipe"
  (sem definição canônica de produtividade), "Saúde das Contas" (Accounts
  permanece com semântica indefinida, ADR-0011).

Este pacote é, na prática, um cenário mais grave que o `bpo`: `bpo` ao
menos persistia linhas reais em tabelas (não religadas); `internal/reports`
não tem camada de persistência alguma — todo caminho de código retorna
dado fabricado ou vazio.

## Decisão

**`internal/reports` é retirado (RETIRE). Não será religado.**

A remoção física do código acontece em uma slice separada e pequena
(`PRODUCT.4-B — Remove Orphaned Legacy Reports Package`), sem afetar
router, migrations ou frontend (nenhum dos três o referencia hoje).

`/reports` permanece `MOCK-CONTAINED` — nenhuma mudança de produto nesta
decisão. O mock atual não define requisitos reais; é apenas contenção
temporária (FRONTEND.1) até que uma capacidade real de Reports seja
desenhada.

## Conceitos preservados (apenas como referência, não como contrato)

- `ReportTemplate` / `ReportColumn` / `ReportFilter`
  (`internal/reports/domain/report.go`) — a forma genérica de "definição
  de relatório com colunas/filtros ordenáveis e filtráveis" é uma ideia
  conceitual razoável para uma futura implementação real, mas **não deve
  ser copiada como código** — deve ser redesenhada contra a arquitetura
  atual se e quando necessário.

## Semânticas explicitamente rejeitadas

- Relatórios de SLA — nenhum modelo de SLA existe no produto hoje.
- Relatórios de saúde de conta — Accounts permanece com domínio
  indefinido (ADR-0011); não decidir isso aqui.
- Relatórios financeiros/produtividade — sem definição canônica.
- O enum `ReportType` (`tickets`/`accounts`/`sla`/`audit`/`financial`) —
  embute os mesmos domínios não resolvidos acima; não reaproveitar.

## Princípio para uma futura capacidade real de Reports

Uma futura implementação real de Reports deve nascer de uma capacidade
canônica concreta e existente, por exemplo:

- exportar a listagem canônica de tickets (já real desde o PRODUCT.2) em
  CSV;
- exportar contatos;
- exportar dados de conversas;
- um resumo operacional estreito e bem definido.

Uma futura Reports **nunca** deve ressuscitar `internal/reports` como uma
segunda verdade. Deve usar:

- dados de domínio canônicos;
- `TenantContext` explícito;
- uma permissão própria e explícita — **não** reaproveitar `dashboard.read`,
  `ticket.read` ou `agent.read` como atalho sem uma decisão de segurança
  dedicada (semânticas diferentes: visibilidade de agregados operacionais
  vs. exportação/relatório são conceitos distintos);
- acesso RLS-safe (mesmo padrão `platformdb.QuerierFromContext` já
  estabelecido em `contacts`/`tickets`/`dashboard`);
- comportamento real de exportação/agregação, nunca dado fabricado;
- testes de integração reais (Postgres + RLS + E2E), nunca apenas
  unitários contra dados inventados.

## Consequências

- Nenhuma migração revertida (nenhuma existe).
- Nenhum comportamento de produto removido — o pacote nunca esteve ativo.
- `/reports` continua exatamente como está (mock-contido) até uma decisão
  de produto separada sobre uma capacidade real de Reports.
- A remoção física do código (`PRODUCT.4-B`) é uma slice de limpeza pura,
  sem risco de isolamento de tenant, sem delta de schema, sem delta de
  rota.

## Não-objetivos desta ADR

- Implementar uma Reports real.
- Definir uma permissão de reports.
- Definir SLA.
- Definir Accounts.
- Definir métricas de produtividade.
