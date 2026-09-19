# Arquitetura Inicial

## Estratégia

Começar como **monólito modular + workers assíncronos**, mantendo limites de domínio claros. Separar serviços somente quando existir necessidade operacional mensurável.

## Stack proposta

- Frontend: React/Next.js + TypeScript.
- Backend: Go (ADR 0008; veja `docs/adr/0008-go-backend-core.md`).
- Banco: PostgreSQL (source of truth; ADR 0006).
- Cache/presença/locks curtos: Valkey (MVP: omitir no R0.1, adicionar em R0.2 quando necessário).
- Jobs/eventos no MVP: NATS JetStream (ADR 0006).
- Realtime: WebSocket/SSE por gateway da aplicação.
- Objetos/anexos: S3-compatible.
- Observabilidade: OpenTelemetry (ADR 0007) + métricas/logs centralizados.
- Infra: containers; Kubernetes somente quando escala/organização justificar.

## Módulos do backend

```text
identity
tenancy
hub
contacts
conversations
tickets
routing
channels
integrations
automation
sla
analytics
audit
realtime
```

## Fronteiras

`channels` traduz provedores para eventos canônicos.  
`integrations` traduz ERPs para operações canônicas.  
`hub` concede escopo, mas não possui conversas/tickets.  
`tenancy` fornece TenantContext e enforcement.  
`audit` recebe eventos relevantes de todos os módulos.

## Fluxo de entrada

```text
Provider Webhook
  -> Channel Adapter
  -> verify + idempotency
  -> normalize message
  -> resolve Tenant by ChannelConnection
  -> Conversation/Ticket
  -> Routing
  -> Domain Event
  -> Realtime
  -> Agent UI
```

## Fluxo de saída

```text
Agent UI
  -> API
  -> AuthN/AuthZ
  -> TenantContext
  -> Conversation command
  -> Outbound message
  -> Channel Adapter
  -> Provider API
  -> status event
```

## Fluxo ERP

```text
UI/Automation
  -> Integration Command
  -> TenantContext
  -> ERP Connection
  -> Canonical Port
  -> IXC Adapter
  -> external API
  -> canonical result
  -> Audit/Event
```

## Estrutura futura sugerida

```text
apps/
  web/
  api/
  worker/
packages/
  domain/
  contracts/
  ui/
  observability/
  integrations/
    ixc/
  channels/
    whatsapp/
    webchat/
tests/
  e2e/
  isolation/
```

## Regra de extração para microserviço

Extrair somente quando houver pelo menos uma razão concreta:
- escala independente;
- blast radius;
- ownership de equipe;
- runtime diferente;
- necessidade de isolamento operacional;
- ciclo de deploy claramente distinto.
