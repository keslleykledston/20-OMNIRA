# Plataforma Operacional — Baseline Recomendada

**Status:** Proposed v0.3  
**Princípio:** contratos preparados para escala; implementação simples por etapa.

## Stack recomendada

| Camada | Escolha inicial | Evolução sem quebra |
|---|---|---|
| Frontend | Next.js + TypeScript | separar apps Console/Hub se necessário |
| Core backend | **Go** | extrair módulos em serviços Go quando houver motivo |
| HTTP | `net/http` + router leve | gateway separado somente quando necessário |
| Persistência | PostgreSQL gerenciado | placements dedicados por Tenant |
| SQL | `pgx` + queries tipadas/geradas | read replicas/partitioning quando medido |
| Cache/presence | **Valkey** | cluster/sentinel/managed |
| Mensageria | **NATS + JetStream** | cluster R3, multi-AZ, mirrors quando necessário |
| Objetos | S3-compatible | replication/versioning/cold tier |
| Telemetria | **OpenTelemetry** | collector/Alloy + backend substituível |
| Métricas | Prometheus | Mimir/managed quando retenção/escala exigir |
| Logs | Loki | object storage / managed |
| Traces | Tempo | managed OTLP-compatible |
| Dashboards | Grafana | managed/self-hosted |
| Alertas | Prometheus Alertmanager | PagerDuty/Opsgenie/etc. como destino |
| IaC | Terraform/OpenTofu | módulos por ambiente |
| Containers | Docker/OCI | Kubernetes só quando operação justificar |

## Escolhas deliberadamente evitadas no MVP

- microserviços obrigatórios;
- service mesh;
- Kafka;
- Kubernetes como pré-requisito;
- workflow engine distribuído pesado;
- API Gateway dedicado sem necessidade;
- Redis/Valkey como source of truth;
- filas armazenando segredos;
- database-per-tenant para todos.

## Topologia lógica

```text
Browser / Channels / Webhooks
           |
      Load Balancer
           |
      OMNIRA API (Go)
      /      |       \
 Postgres   NATS    Valkey
    |        |         |
  Truth    Events     Cache/
 Outbox     Jobs      Presence
    |
Object Storage
    |
attachments/backups
```

## Regra de evolução

Uma tecnologia nova entra quando:
1. há gargalo medido, requisito funcional ou requisito operacional;
2. a interface existente não resolve;
3. existe owner e runbook;
4. o ganho supera o custo operacional.
