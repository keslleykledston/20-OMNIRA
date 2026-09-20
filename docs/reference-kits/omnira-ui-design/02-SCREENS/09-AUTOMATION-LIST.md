# 09 AUTOMATION LIST

**Rota:** `/app/automation`  
**Referência visual:** `../references/10-automation-list.png`  
**Papéis:** Admin; Supervisor conforme permissão.

## Propósito

Listar, filtrar e gerenciar fluxos de automação, estados e versões.

## Wireframe

```text
APP SHELL
└─ PAGE
   ├─ Header + New automation
   ├─ 4 summary metrics
   ├─ Status tabs + search + filters
   └─ Automations table
```

## Layout e regiões

- Quatro metric cards no topo.
- Tabs: Todas, Ativas, Rascunhos, Pausadas.
- Busca central e filtros Trigger/Status.
- Tabela com ícone temático suave por automação.

## Funcionalidades e interações

Criar automação.  
Abrir editor ao clicar.  
Menu: duplicar, pausar/ativar, ver versões, arquivar/excluir conforme política.  
Status: Ativa, Rascunho, Pausada.  
Published version é imutável.

## Dados necessários

AutomationFlow summary: id, name, description, triggerType, status, publishedVersion, executionCount, updatedAt, updatedBy. Metrics: total, active, executionsToday, resolution rate se calculável.

## Estados obrigatórios

Loading, empty, no results, error, permission. Pausar/ativar mostra progress e rollback em erro.

## Responsividade

Tablet mantém tabela simplificada; mobile usa cards com nome, trigger, status, versão, execuções e updatedAt.

## Component map conceitual

`MetricCard`, `Tabs`, `SearchField`, `Select`, `AutomationRow`, `StatusBadge`, `DropdownMenu`.

## Checklist de fidelidade

- mesma linguagem visual de Contacts/Tickets;
- ícones coloridos só em tiles pequenos;
- status pills suaves;
- CTA primário no topo direito.
