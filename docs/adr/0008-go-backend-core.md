# ADR 0008 — Go como backend principal do core

**Status:** Accepted  
**Data:** 2026-09-18

## Contexto

OMNIRA precisa suportar:
- APIs HTTP;
- webhooks;
- WebSockets/SSE;
- workers concorrentes;
- integrações externas;
- mensageria;
- alto volume de I/O;
- operação previsível em containers.

O MVP deve permanecer simples, mas sem criar uma base que exija reescrita do core ao crescer.

## Decisão

Adotar **Go** como linguagem principal do backend e dos workers do core OMNIRA.

Baseline:
- Go atual suportado pelo time;
- `net/http`;
- router leve;
- `pgx` para PostgreSQL;
- queries tipadas/geradas quando vantajoso;
- OpenTelemetry;
- NATS JetStream;
- Valkey;
- OpenAPI/AsyncAPI.

## Regras

- evitar framework pesado que recrie um container de DI complexo;
- domínio não depende de HTTP, NATS ou PostgreSQL;
- adapters implementam ports;
- módulos permanecem explícitos;
- workers podem compartilhar binário inicialmente;
- Python pode entrar futuramente para IA/dados sem substituir o core.

## Consequências

### Positivas
- concorrência simples;
- bom uso de CPU/memória;
- binários pequenos;
- deploy simples;
- ótimo fit para realtime, webhooks, integrações e workers.

### Custos
- exige disciplina arquitetural sem framework opinativo;
- algumas ferramentas de alto nível disponíveis em outros ecossistemas precisarão de escolhas explícitas.

## Alternativas

- NestJS/TypeScript: mantido como alternativa válida, mas não selecionada para o core.
- FastAPI/Python: mantido para serviços futuros de IA/dados, não para o core principal.
