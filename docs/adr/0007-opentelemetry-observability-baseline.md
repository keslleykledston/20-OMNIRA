# ADR 0007 — OpenTelemetry como padrão de instrumentação

**Status:** Proposed  
**Data:** 2026-09-18

## Decisão

Instrumentar API/workers/adapters com OpenTelemetry.

Backend inicial recomendado:
- Prometheus metrics;
- Loki logs;
- Tempo traces;
- Grafana dashboards;
- Alertmanager notifications.

A aplicação não depende diretamente dos backends; envia OTLP/telemetria através do collector.

## Regras

- correlation/trace propagado em HTTP, NATS e tool executions;
- PII mínima;
- secrets nunca;
- evitar labels de alta cardinalidade em metrics;
- dashboards e alertas fazem parte da Definition of Done para componentes críticos.
