# Disaster Recovery / Business Continuity

## Objetivo inicial

Sem multi-region ativo-ativo no MVP.

Começar com:
- serviços stateless;
- banco gerenciado Multi-AZ quando disponível;
- JetStream R3 em domínios de falha independentes;
- object storage durável;
- IaC reproduzível;
- backups testados.

## Fontes de verdade

| Componente | Fonte de verdade? | Recuperação |
|---|---|---|
| PostgreSQL | SIM | PITR/base backup/WAL |
| Object Storage | SIM para anexos | versioning/replication/backup |
| NATS JetStream | transporte durável | replicas + snapshot; reconstrução parcial por Postgres/outbox |
| Valkey | NÃO | recriar |
| App containers | NÃO | redeploy por IaC/image |
| Observability | não para negócio | retenção/backup conforme política |

## Target inicial

Para produção piloto:

- **RPO alvo do core:** <= 5 minutos.
- **RTO alvo:** <= 60 minutos.

Esses são objetivos operacionais; só viram SLA contratual após testes de restore demonstrarem capacidade real.

## PostgreSQL

- PITR via WAL + base backup do serviço gerenciado.
- backup diário adicional conforme política.
- retenção definida por ambiente.
- restore em ambiente isolado.
- teste de restauração recorrente.

PITR é preferível a depender apenas de `pg_dump`.

## NATS JetStream

Produção:
- 3 nós/replicas para streams críticos;
- espalhar replicas por failure domains;
- durable consumers;
- monitorar quorum/replicas;
- snapshots conforme criticidade.

O estado de negócio de jobs/tools permanece no PostgreSQL, permitindo reconciliar/re-enfileirar execuções após desastre.

## Valkey

Tratar como descartável no MVP:
- presence;
- cache;
- rate-limit auxiliar;
- locks curtos quando apropriado.

Nenhuma informação irreconstituível pode existir somente no Valkey.

## Object Storage

- versioning;
- checksum;
- lifecycle;
- proteção contra exclusão acidental;
- replication/cópia secundária quando contrato exigir.

## Recovery order

```text
1. network/secrets/identity
2. PostgreSQL
3. object storage access
4. NATS
5. API
6. workers
7. realtime
8. Valkey/cache
9. external adapters
10. observability validation
```

## Reconciliation pós-restore

Executar:
- outbox não publicada -> publicar;
- ToolExecution `QUEUED/RUNNING` sem heartbeat -> reconciliar;
- webhook inbox não processada -> retomar;
- mensagens outbound sem status terminal -> consultar/reconciliar quando provider suportar;
- nunca reenviar operação não idempotente cegamente.

## Drill

MVP:
- restore test antes do primeiro piloto;
- depois, pelo menos trimestral;
- registrar RPO/RTO observado;
- corrigir runbook.

## Incidentes de segurança

Cross-tenant não é tratado como simples falha técnica:
- preservar evidências;
- bloquear grants/sessões afetadas;
- avaliar exposição;
- comunicar conforme processo jurídico/compliance aplicável;
- criar teste regressivo.
