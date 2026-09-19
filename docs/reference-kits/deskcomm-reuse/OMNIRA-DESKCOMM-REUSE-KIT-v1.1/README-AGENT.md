# Instruções ao agente implementador

Você recebeu um kit de reaproveitamento do DeskcommCRM para o OMNIRA.

## Missão

Aproveitar o conhecimento e, quando seguro, código do DeskcommCRM para reduzir tempo de construção do OMNIRA, **sem violar as decisões arquiteturais do OMNIRA**.

## Regras

### 1. Nunca converter o OMNIRA em fork

Não:
- substituir Go por Next Route Handlers;
- adotar Supabase como backend obrigatório;
- substituir NATS por `event_log` como fila principal;
- substituir Nginx por Caddy;
- introduzir WAHA como canal principal;
- substituir OpenTelemetry por Sentry como fundação.

### 2. Audite antes de portar

Para cada módulo:

```text
source path
source commit
purpose
dependencies
tenant assumptions
security assumptions
runtime assumptions
classification
OMNIRA target
tests to port
```

### 3. Portar conhecimento, não dependência

Exemplo:

```text
Deskcomm:
lib/channels/adapters/meta-cloud.ts

OMNIRA:
internal/channels/meta/
```

O código TypeScript é referência de comportamento.
A implementação backend final é Go.

### 4. Frontend

Código React/Next pode ser reaproveitado quando fizer sentido.

Porém:
- trocar data access para API Go;
- remover dependência Supabase do componente;
- aplicar Omnira iOS Design System;
- preservar acessibilidade;
- não copiar branding Deskcomm.

### 5. Segurança

Antes de aceitar um port:
- TenantContext;
- RLS;
- cross-tenant test;
- IDOR/BOLA;
- secret handling;
- webhook authenticity;
- idempotência;
- SSRF quando existir fetch externo.

### 6. Attribution

DeskcommCRM é MIT.

Se copiar/adaptar código ou partes substanciais:
- registrar em `THIRD_PARTY_NOTICES.md`;
- preservar aviso MIT aplicável;
- registrar origem no `docs/research/deskcomm/SOURCE-MAP.md`.

### 7. Não antecipar módulos

Siga `docs/MIGRATION-WAVES.md`.

### 8. Report padrão

Ao fim de cada slice:

```text
Slice:
Classification:
Deskcomm source:
Source commit:
OMNIRA target:
Implemented:
Tests ported:
Security checks:
License/attribution:
Docs:
Remaining gaps:
Next slice:
```

## WhatsApp multi-provider

O OMNIRA deve manter suporte arquitetural a WhatsApp oficial e não oficial.

- official é o caminho padrão;
- unofficial é opt-in por Tenant;
- todo provider implementa o contrato canônico;
- o domínio não conhece QR/session/browser;
- providers com runtime próprio rodam em containers opcionais;
- nunca fazer fallback silencioso entre categorias.
