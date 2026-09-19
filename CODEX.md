# Instruções para Codex — OMNIRA

Comece por `START-HERE.md`.

## Missão

Implementar o OMNIRA incrementalmente conforme os documentos canônicos.

## Hierarquia de autoridade

Quando houver conflito:

1. ADR Accepted;
2. `CONTEXT.md`;
3. `docs/product/MVP.md`;
4. spec do release atual;
5. arquitetura;
6. ticket;
7. implementação existente.

Não resolva conflito silenciosamente; registre-o.

## Loop por tarefa

```text
read spec
-> inspect code
-> define acceptance tests
-> implement smallest complete slice
-> run tests
-> run isolation/security checks
-> update docs/contracts/changelog
-> report result
```

## Limites

Não faça refactor amplo antes de completar o comportamento solicitado.
Não crie framework interno.
Não adicione abstrações sem caso de uso atual ou boundary real.

## Regras de segurança

- construir `TenantContext` somente após autorização;
- backend sempre revalida membership/grant;
- jobs carregam IDs, não secrets;
- logs/traces não contêm tokens;
- nenhuma operação cross-tenant fora de um fluxo explicitamente autorizado.

## Infra

Use inicialmente:
- Go;
- PostgreSQL;
- NATS JetStream;
- OpenTelemetry;
- Next.js;
- Nginx.

Valkey entra quando R0.2 exigir cache/presence.

## Entrega

Cada tarefa precisa terminar com:
- testes verdes;
- código compilando;
- docs coerentes;
- changelog quando aplicável;
- nenhuma TODO crítica escondida.
