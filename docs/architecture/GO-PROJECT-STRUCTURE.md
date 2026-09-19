# Estrutura Go do OMNIRA

## Diretriz

Organizar por domínio/módulo, não por tipo global (`controllers/`, `services/`, `repositories/`).

```text
cmd/
  api/
  worker/

internal/
  identity/
    domain/
    application/
    ports/
    adapters/
  tenancy/
  audit/
  outbox/
  platform/
    authn/
    db/
    messaging/
    telemetry/
    httpserver/

contracts/
migrations/
tests/
```

## Dependency rule

```text
domain
  ↑
application
  ↑
ports
  ↑
adapters/platform
```

Domínio:
- não importa HTTP;
- não importa pgx;
- não importa NATS;
- não importa OTel.

## Módulo

Cada módulo pode conter:

```text
domain/
  entity.go
  errors.go
  policy.go

application/
  commands.go
  queries.go
  service.go

ports/
  repository.go
  publisher.go

adapters/
  postgres/
  http/
  nats/
```

Não exigir todas as pastas quando o módulo ainda é pequeno.

## Error model

Erros de domínio/aplicação devem ser estáveis:

```text
not_found
forbidden
conflict
validation_error
external_dependency_error
rate_limited
```

Adapters traduzem esses erros para HTTP/eventos.

## Transactions

Application service delimita transação de caso de uso.

Repository não começa transação escondida.

## Interfaces

Criar interfaces onde existe boundary real/testável.

Evitar interface para cada struct “por padrão”.

## Dependency injection

Wiring explícito no `main` ou pacote de bootstrap.

Evitar container DI reflexivo no MVP.

## Logging

Logger estruturado injetado em platform/application boundary.

Domínio puro não precisa logar.

## Generics

Usar somente quando reduzir duplicação real. Não criar framework interno abstrato.
