# Governança de API

## Superfícies

```text
/api/v1/...                 API pública do Console/Hub
/webhooks/v1/<provider>     entrada de provedores
/realtime/v1/...            WebSocket/SSE
/internal/health/...        health probes
/internal/metrics           scrape protegido
/internal/admin/...         operações internas, nunca públicas
```

## REST primeiro

REST é o contrato principal do MVP. GraphQL não é necessário.

### Convenções
- IDs internos UUID/ULID.
- cursor pagination para listas grandes.
- filtros explícitos.
- UTC/RFC3339.
- `Idempotency-Key` em comandos sensíveis/repetíveis.
- `correlation_id` devolvido em erro/resposta.
- erros em formato Problem Details.
- limites de payload e upload definidos.
- timeout por endpoint.

## Autorização

O cliente não escolhe autoridade por `tenant_id`.

```text
session
 -> requested resource
 -> authorization
 -> TenantContext
 -> handler/repository
```

Deep links passam pela mesma autorização.

## Versionamento

- `/api/v1` representa versão de contrato.
- adicionar campo opcional não cria v2.
- remover/renomear/mudar semântica exige v2 ou janela de depreciação.
- webhooks e eventos também têm versão.

## Webhooks

Obrigatório por provider:
- validação de assinatura;
- timestamp/replay window quando disponível;
- idempotência por provider message/event id;
- ACK rápido;
- processamento pesado assíncrono;
- raw payload protegido somente se necessário para debugging/compliance.

## Realtime

Realtime é atualização de visão, não fonte de verdade.

Após reconnect:
1. cliente reabre conexão;
2. recebe cursor/version;
3. sincroniza estado perdido via API;
4. continua realtime.

## Contratos como código

```text
contracts/
  openapi/
    public-v1.yaml
  asyncapi/
    events-v1.yaml
  schemas/
```

CI deve validar contratos e detectar breaking changes.
