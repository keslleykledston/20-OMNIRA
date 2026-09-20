# 11 REPORTS

**Rota:** `/app/reports`  
**Referência visual:** `../references/09-reports.png`  
**Papéis:** Admin, Supervisor; Agent geralmente sem acesso global.

## Propósito

Analytics operacional para atendimento, operadores, filas, canais e tickets.

## Wireframe

```text
APP SHELL
└─ PAGE
   ├─ Header + Export
   ├─ Global filters (period/channel/queue/operator)
   ├─ Tabs
   ├─ KPI row (6)
   ├─ Chart grid
   │  ├─ volume by hour
   │  ├─ volume by day/channel
   │  └─ TMA/TME trend
   └─ Operators table
```

## Layout e regiões

- Filtros globais em 4 colunas.
- Seis KPI cards compactos.
- Dois gráficos lado a lado, um gráfico full-width abaixo.
- Tabela operacional no rodapé.
- Cards e charts usam borda sutil; gridlines leves.

## Funcionalidades e interações

Tabs: Atendimento, Operadores, Filas, Canais, Tickets.  
Filtros atualizam todos widgets.  
Exportar respeita filtro e permissão.  
Tooltips de métricas explicam TME/TMA/SLA.  
Operadores/filas são clicáveis para drill-down futuro.

## Dados necessários

Metrics: conversations, tickets, avgWait/TME, avgHandle/TMA, SLA%, resolution%.  
Series temporais por intervalo.  
Operator rows: conversations, tickets, TMA, TME, SLA, resolutionRate. Datas/percentuais formatados localmente.

## Estados obrigatórios

Loading widgets independentemente; sem dados no período; export processing/error; permission; partial data warning.

## Responsividade

Tablet empilha charts; mobile KPIs 2 colunas e charts full-width. Filtros viram Sheet/accordion.

## Component map conceitual

`ReportFilterBar`, `MetricCard`, `ChartCard`, `Tabs`, `OperatorPerformanceTable`, `ExportButton`.

## Checklist de fidelidade

- seis metrics com ícones suaves;
- gráficos densos mas limpos;
- legenda compacta;
- não usar cores aleatórias fora da paleta semântica/canais;
- tabela final preserva leitura.
