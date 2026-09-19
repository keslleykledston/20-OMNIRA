# Docker-First — Decisão congelada

**Status:** Accepted  
**Data:** 2026-09-18

## Decisão

Toda a plataforma OMNIRA deve ser construída e operada em containers Docker.

Docker não é apenas ambiente de desenvolvimento: é o padrão de empacotamento e execução dos módulos/serviços do projeto.

## Componentes obrigatoriamente containerizados

```text
omnira-nginx          (frontend reverse proxy, TLS)
omnira-web            (Next.js frontend)
omnira-api            (Go backend API)
omnira-worker         (Go workers assíncronos)
omnira-postgres       (PostgreSQL)
omnira-nats           (NATS JetStream)
omnira-otel           (OpenTelemetry Collector)
omnira-prometheus     (metricas, quando necessário)
omnira-loki           (logs centralizados, quando necessário)
omnira-tempo          (traces distribuídos, quando necessário)
omnira-grafana        (dashboards, quando necessário)
```

Valkey entra quando for necessário pela release correspondente.

## Ambiente host

O host deve possuir essencialmente:

```text
Docker Engine
Docker Compose plugin
configuração de rede/firewall
diretórios/volumes necessários
```

Não instalar Go, Node.js, PostgreSQL, NATS ou demais runtimes diretamente no servidor de produção como requisito da aplicação.

## Imagens separadas por workload

```text
omnira-web           (frontend, Next.js)
omnira-api           (API, Go)
omnira-worker        (workers, Go)
omnira-nginx         (proxy/TLS)
```

Mesmo que `api` e `worker` compartilhem o mesmo código Go, podem usar a mesma imagem base e comandos/entrypoints diferentes.

**Regra:** Não criar microserviços apenas porque Docker facilita criar containers. Containers são unidade operacional; não obrigatoriamente boundaries de domínio. A arquitetura continua sendo monólito modular + workers.

## Dockerfiles — Requisitos

### Go (API, Worker)

Usar multi-stage build:

1. **Builder stage:** compile Go
2. **Runtime stage:** somente binário + certificados/dependências mínimas

Requisitos:

- Imagem final pequena.
- Executar como usuário não-root.
- Nenhuma toolchain Go na imagem final.
- Graceful shutdown implementado.
- Healthcheck quando apropriado.
- Versão/build metadata disponíveis (via `-ldflags`).

### Next.js (Web)

Usar multi-stage build:

1. **Builder stage:** `npm ci` + `npm run build`
2. **Runtime stage:** apenas dependências de runtime

Requisitos:

- Output standalone quando adequado.
- Imagem final não-root.
- Nenhuma dependência de desenvolvimento.
- Pequena.

### Nginx

Configuração versionada dentro do repositório e empacotada em imagem/config própria.

Requisitos:

- TLS ready.
- Non-root.
- Small.
- Healthcheck.

## Docker Compose

### Estrutura

```text
deploy/
  docker/
    compose.yml           (production-like, com services principais)
    compose.dev.yml       (desenvolvimento, com overrides)
    .env.example          (template de variáveis)
    nginx/
      Dockerfile
      nginx.conf
    otel/
      otel-collector-config.yml
    postgres/
      Dockerfile (se necessário init customizado)
    loki/
    prometheus/
    tempo/
    grafana/
```

### Uso

```bash
docker compose up -d
docker compose down
```

Deve ser suficiente para iniciar/parar após configuração do `.env`.

## Redes

Separar redes quando melhorar segurança sem criar complexidade artificial:

```text
public
  ├── nginx

application
  ├── nginx
  ├── web
  ├── api

backend
  ├── api
  ├── worker
  ├── postgres
  ├── nats
  ├── otel-collector
  └── prometheus
```

PostgreSQL, NATS, endpoints internos e stack de observabilidade **não devem ser publicados diretamente para a Internet**.

Somente Nginx expõe publicamente a aplicação.

## Roteamento público

Domínio: `https://omnira.devops.k3gsolutions.com.br`

```text
Internet → Nginx (port 443)
           ├── GET /                    → omnira-web
           ├── POST /api/v1/*           → omnira-api
           ├── POST /webhooks/v1/*      → omnira-api
           ├── WS  /realtime/v1/*       → omnira-api
```

## Endpoints privados (não expor)

Nunca publicar para Internet sem autenticação administradora explícita:

```text
/internal/*
/metrics
/debug/*
/pprof/*
PostgreSQL management
NATS management console
Grafana/Prometheus/Loki/Tempo
```

## TLS

- HTTPS é **obrigatório** no domínio público.
- Preparar Nginx para certificado TLS (montado via volume/secret).
- **Não embutir certificados privados na imagem ou no Git.**

## Persistência

Containers são descartáveis; estado persistente em volumes ou serviços dedicados:

### PostgreSQL

- Volume persistente **obrigatório**.
- Strategy: managed backup + PITR quando disponível.

### NATS JetStream

- Volume persistente **obrigatório** quando JetStream ativo.
- Snapshots conforme criticidade.

### Object Storage

- Persistência apropriada/externa.

### Observabilidade

- Volumes conforme necessidade.

### Valkey

- **Não tratar como source of truth** mesmo com volume.
- Nunca depender de filesystem efêmero para informação de negócio.

## Secrets

**Nunca colocar secrets em:**

- Dockerfile
- Imagem
- Git
- Compose versionado
- Build arguments
- Logs

**Usar:**

- Variáveis de ambiente seguras.
- `.env` fora do Git (apenas para dev).
- Docker secrets ou secret manager quando disponível.
- Fornecer `.env.example` com nomes/placeholders.

## Healthchecks

Cada serviço relevante deve possuir health/readiness apropriado:

```text
omnira-api      (GET /internal/health/live, /ready)
omnira-worker   (liveness logic interno)
omnira-postgres (pg_isready)
omnira-nats     (nc localhost:4222)
omnira-web      (health listener opcional)
omnira-nginx    (health check interno)
omnira-otel     (port 13133 /healthz)
```

**Regra:** Não confundir processo vivo com aplicação pronta. Compose pode usar `depends_on`/healthchecks, mas a aplicação também precisa tolerar dependências indisponíveis e reconectar.

## Logs

Containers devem escrever logs estruturados em:

```text
stdout
stderr
```

**Não depender de arquivos de log locais dentro do container.**

OpenTelemetry continua sendo o padrão de telemetria.

## Migrations

Migrations também devem poder ser executadas via container.

Exemplo conceitual:

```bash
docker compose run --rm migrate up
```

ou comando equivalente usando imagem da aplicação.

**Não exigir ferramenta instalada no host.**

## Backup e Restore

Scripts/tools de backup e restore devem operar considerando os containers.

Comandos reproduzíveis para:

```text
backup postgres
restore postgres
validar restore
snapshot/recovery JetStream
```

T13 deve funcionar no ambiente Docker.

## Testes

Testes de integração e isolation devem usar dependências reais containerizadas:

```text
PostgreSQL
NATS
```

**Não criar mocks** para substituir justamente as propriedades que queremos validar (RLS/isolation).

## CI/CD

Build do CI deve validar:

```bash
docker build omnira-api -t omnira-api:$VERSION
docker build omnira-web -t omnira-web:$VERSION
docker build omnira-nginx -t omnira-nginx:$VERSION
docker compose config  (validar sintaxe)
integration tests
isolation tests
smoke tests
```

Imagens versionáveis por:

```text
git SHA
release version
```

**Não depender apenas de `latest`.**

## Segurança dos containers

Por padrão:

- Usuário **não-root**.
- Filesystem read-only quando tecnicamente adequado.
- Capabilities mínimas.
- **Sem `--privileged`**.
- **Sem montar Docker socket** na aplicação.
- **Sem secrets na imagem.**
- Imagens base pequenas e mantidas.
- Dependency/image scanning no CI quando possível.

**Não transformar hardening em bloqueio do MVP.** Aplicar primeiro os controles simples e de alto valor.

## Desenvolvimento

O desenvolvedor pode usar ferramentas locais para produtividade, mas deve existir sempre um caminho oficial Docker reproduzível.

### Critério de aceitação — NÃO

"Funciona na minha máquina porque tenho Go/Postgres/NATS instalados."

### Critério de aceitação — SIM

"Funciona a partir do repositório usando o ambiente Docker documentado."

## Automation-First + Docker

Integre Docker à política de skills/tools.

Tools reutilizáveis:

```text
tools/dev-up              (docker compose up + health)
tools/dev-down            (docker compose down)
tools/test                (docker compose run tests)
tools/test-isolation      (docker compose run isolation tests)
tools/migrate             (docker compose run migrate)
tools/smoke               (docker compose smoke tests)
tools/backup              (docker exec postgres backup)
tools/restore             (docker exec postgres restore)
tools/doctor              (docker compose config + health)
```

Se a mesma sequência Docker for repetida 2+ vezes, transforme em tool/script.

## Kubernetes

Docker-first **não significa Kubernetes agora.**

**Não introduzir:**

```text
Kubernetes
Helm
service mesh
operators
```

no MVP sem requisito comprovado + ADR aprovado.

Docker Compose é suficiente para as primeiras entregas.

A arquitetura deve permitir migração futura para orchestrator sem alterar código de domínio.

## Definition of Done

A partir de agora, um componente executável não é considerado pronto se:

- ✗ Não possuir build Docker reproduzível.
- ✗ Depender de runtime instalado manualmente no host de produção.
- ✗ Não ter configuração externalizada (env vars).
- ✗ Persistir informação de negócio apenas no filesystem efêmero.
- ✗ Expuser serviço interno desnecessariamente.
- ✗ Não participar do fluxo de health/telemetria apropriado.
- ✗ Usar `latest` tag sem versionamento.

## Referências

- `docker-compose.yml` — exemplo de estrutura.
- `tools/dev-up.sh` — startup reproduzível.
- `docs/development/LOCAL-SETUP.md` — setup local.
- `Dockerfile` exemplos — quando forem criados.
