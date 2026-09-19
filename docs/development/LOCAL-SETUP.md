# Local Development Setup

## Pré-requisitos

- Go 1.22+
- Docker + Docker Compose
- Make
- PostgreSQL client tools (opcional, para debug)
- curl, nc (para health checks)

## Quick Start

### 1. Start infrastructure
```bash
./tools/dev-up.sh
```

This will:
- Start Postgres 16, NATS 2.10 with JetStream, OTel Collector
- Wait for all services to be healthy
- Display connection endpoints

### 2. Run API
```bash
make run-api
```

API starts on `:8080` (can override with `OMNIRA_HTTP_ADDR=:9000`).

### 3. Test health endpoints
```bash
curl http://localhost:8080/internal/health/live
curl http://localhost:8080/internal/health/ready
curl http://localhost:8080/internal/health/modules
```

### 4. Stop infrastructure
```bash
./tools/dev-down.sh
```

## Service endpoints

| Service | Address | Purpose |
|---------|---------|---------|
| Postgres | `localhost:55434` | Database |
| NATS | `localhost:4222` | Messaging/JetStream |
| OTel GRPC | `localhost:4317` | Telemetry ingestion |
| OTel HTTP | `localhost:4318` | Telemetry ingestion (HTTP) |
| OTel Health | `localhost:13133` | Health check endpoint |

## Credentials

| Service | User | Password | Database |
|---------|------|----------|----------|
| Postgres | `omnira` | `omnira` | `omnira_dev` |

## Common tasks

### Check infrastructure health
```bash
./tools/check-docker-health.sh
```

### View service logs
```bash
docker compose logs -f [postgres|nats|otel-collector]
```

### Connect to Postgres
```bash
psql -h localhost -U omnira -d omnira_dev
```

### Run tests
```bash
make test
```

### Format code
```bash
make fmt
make vet
```

### Build binaries
```bash
make build
```

## Troubleshooting

### Port already in use
Change port in docker-compose.yml or stop conflicting service:
```bash
docker ps
docker stop <container_id>
```

### Postgres not ready
Check logs:
```bash
docker compose logs postgres
```

### NATS connection refused
Ensure NATS is running:
```bash
docker compose ps nats
```

### OTel Collector not responding
Verify config in `config/otel-collector-config.yml` and restart:
```bash
docker compose restart otel-collector
```

## Volume management

Data is persisted in Docker volumes:
- `postgres_data` — Postgres data files
- `nats_data` — NATS JetStream files

To reset:
```bash
docker compose down -v
./tools/dev-up.sh
```

## CI/CD considerations

- All services must pass health checks before tests run.
- `tools/check-docker-health.sh` returns exit code 0 on success, 1 on failure.
- Use `docker compose` commands in CI without interactive flags.
