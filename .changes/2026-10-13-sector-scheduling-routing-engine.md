# Setores, calendários, escalas, plantões, transbordo e SLA (ADR-0044) — branch, não implantado

Mudança visível ao usuário **quando for mesclada e a flag `OMNIRA_ROUTING_ENGINE_ENABLED` for ligada** (desligada por padrão; com ela desligada o roteamento atual não muda):

- **Setores** com várias filas; **calendários** operacionais (24x7, comercial, personalizado; feriados, exceções, suspensão emergencial; fuso IANA e horário de verão); calendário padrão da empresa.
- **Escala individual** do agente, **plantões** e **equipes de contingência**; regra única de **elegibilidade** (agenda do setor, escala, presença e capacidade são independentes).
- **Padrão da empresa**: todos os atendentes disponíveis 24x7 e membros do Suporte, que é a fila padrão de transbordo.
- **Motor de roteamento** (atribuir, enfileirar, aguardar abertura, exigir ação manual), estratégia **menos carregado**, timers duráveis.
- **Transbordo em cadeia** (fora do expediente, sem agente, capacidade, espera máxima, SLA em risco/violado) para fila, plantão, agente, setor ou Hub (a conversa nunca sai da empresa).
- **SLA** por empresa, setor ou fila (primeira resposta, espera na fila, atribuição, resolução), corrido ou em horário útil; uma transferência nunca reinicia o relógio.
- Novas permissões: `routing.override` (só administrador). Eventos: `job.routing.sla_at_risk.v1`, `job.routing.sla_breached.v1`.
- Migrations 124–131 (aditivas; ver o guia de rollback em cada `.down.sql`).
