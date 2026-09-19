# Compatibilidade Arquitetural

## DeskcommCRM

Conceitualmente:

```text
Next.js App
 ├─ UI
 ├─ Route Handlers
 ├─ Supabase Auth
 ├─ Supabase Postgres/RLS
 ├─ Supabase Realtime
 └─ Workers/cron
```

## OMNIRA

```text
Nginx
 ├─ Next.js frontend
 └─ Go API
      ├─ PostgreSQL
      ├─ NATS JetStream
      ├─ Workers Go
      └─ OpenTelemetry
```

## Mapeamento

```text
Deskcomm Route Handler
        ↓
não copiar
        ↓
Go application service + HTTP handler
```

```text
Deskcomm Supabase query
        ↓
extrair intenção + constraints
        ↓
Go repository + pgx + RLS
```

```text
Deskcomm event_log worker
        ↓
extrair event semantics
        ↓
Outbox + NATS JetStream + worker
```

```text
Deskcomm React component
        ↓
ADAPT
        ↓
OMNIRA Next.js + Go API + Omnira UI
```

## Regra de fronteira

Nenhum código de frontend pode decidir autorização.

Nenhum adapter externo decide regra de negócio.

Adapters:
- traduzem formato;
- chamam provider;
- devolvem resultado normalizado.

Regras:
- window policy;
- routing;
- retries;
- permissions;
- TenantContext;
- budget;
- handoff;

ficam fora do adapter.
