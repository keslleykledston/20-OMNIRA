# Runbook Inicial

## WhatsApp webhook atrasado
- verificar saúde do endpoint;
- taxa de erro;
- assinatura/verification;
- backlog de jobs;
- DLQ;
- latência do provedor.

## Falha IXC
- identificar Tenant e operação;
- verificar timeout/rate limit;
- circuit breaker;
- credencial;
- não reenviar operação não idempotente sem confirmação.

## Suspeita cross-tenant
Classificar como incidente de máxima severidade:
- preservar logs/auditoria;
- revogar grants/sessões afetadas;
- bloquear export;
- identificar recursos acessados;
- corrigir e adicionar teste regressivo obrigatório.


## Backlog NATS crescendo
- verificar consumer health e replicas;
- identificar oldest pending age;
- separar falha do worker de falha do provedor;
- reduzir retry storm;
- escalar pool se saturação interna;
- não apagar stream para "resolver" backlog.

## ToolExecution preso em RUNNING
- verificar heartbeat/worker;
- identificar se operação é idempotente;
- consultar provedor quando possível;
- reconciliar estado;
- re-enfileirar somente com segurança.

## Restore
Seguir `DISASTER-RECOVERY.md`.
Nunca declarar backup válido sem teste de restore.
