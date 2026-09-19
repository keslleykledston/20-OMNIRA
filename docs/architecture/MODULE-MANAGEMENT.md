# Gestão de Módulos

## Estratégia

Monólito modular com boundaries explícitos. Um módulo pode virar serviço sem mudar seus contratos externos.

Módulos iniciais:

```text
identity
tenancy
audit
contacts
conversations
tickets
routing
channels
integrations
tools
realtime
hub          # entra após tenant core
sla
automation
analytics
```

## Contrato de módulo

Cada módulo declara:
- responsabilidades;
- comandos;
- queries;
- eventos publicados;
- eventos consumidos;
- tabelas que possui;
- dependências permitidas;
- health checks;
- métricas;
- migrations.

## Regra de dados

Um módulo não escreve diretamente nas tabelas de outro módulo.

Leitura cross-module:
1. API/port do módulo;
2. read model explícito;
3. query compartilhada somente quando documentada como exceção.

## Feature/capability flags

Separar:
- módulo implantado;
- capability habilitada globalmente;
- capability contratada/habilitada no Tenant.

Não usar feature flag como autorização.

## Health

Endpoints:

```text
/internal/health/live
/internal/health/ready
/internal/health/modules
```

### live
Processo está vivo.

### ready
Pode receber tráfego; dependências críticas atendem requisito mínimo.

### modules
Diagnóstico detalhado protegido:
- postgres;
- nats;
- valkey;
- object storage;
- adapters relevantes;
- migration version;
- queue health.

## Estado degradado

Falha do IXC não torna toda API unready.

Classificar dependências:
- critical: Postgres;
- important: NATS para operações assíncronas;
- optional/degradable: IXC específico, WhatsApp de um Tenant, analytics.

## Extração de serviço

Somente quando houver:
- escala independente;
- blast radius relevante;
- owner/equipe;
- requisitos de runtime diferentes;
- deploy independente com benefício real.
