.PHONY: help build test run clean fmt vet deps-check docker-up docker-down benchmark benchmark-health benchmark-tenancy

help:
	@echo "OMNIRA Makefile"
	@echo ""
	@echo "targets:"
	@echo "  build              - build API and worker binaries"
	@echo "  test               - run unit tests"
	@echo "  fmt                - format code"
	@echo "  vet                - run go vet"
	@echo "  run-api            - run API locally (dev)"
	@echo "  run-worker         - run worker locally (dev)"
	@echo "  docker-up          - start docker-compose (postgres, nats, otel)"
	@echo "  docker-down        - stop docker-compose"
	@echo "  clean              - remove binaries and temp files"
	@echo "  deps-check         - check for security/license issues"
	@echo ""
	@echo "performance & testing:"
	@echo "  benchmark          - run baseline performance benchmarks"
	@echo "  benchmark-health   - load test health endpoints (k6)"
	@echo "  benchmark-tenancy  - load test tenancy endpoints (k6)"

build:
	@echo "Building API..."
	@go build -o bin/omnira-api ./apps/api/cmd/omnira-api
	@echo "Building Worker..."
	@go build -o bin/omnira-worker ./apps/worker/cmd/omnira-worker

test:
	@go test -v -race -cover ./...

fmt:
	@go fmt ./...

vet:
	@go vet ./...

run-api: docker-up
	@OMNIRA_ENV=development OMNIRA_HTTP_ADDR=:8080 OMNIRA_DATABASE_URL="postgres://omnira:omnira@localhost:55434/omnira_dev" OMNIRA_NATS_URL=nats://localhost:4222 OMNIRA_OTEL_ENDPOINT=http://localhost:4317 go run ./apps/api/cmd/omnira-api

run-worker: docker-up
	@OMNIRA_ENV=development OMNIRA_DATABASE_URL="postgres://omnira:omnira@localhost:55434/omnira_dev" OMNIRA_NATS_URL=nats://localhost:4222 OMNIRA_OTEL_ENDPOINT=http://localhost:4317 go run ./apps/worker/cmd/omnira-worker

docker-up:
	@docker compose -f docker-compose.yml up -d
	@echo "Waiting for services to be ready..."
	@sleep 5

docker-down:
	@docker compose -f docker-compose.yml down

clean:
	@rm -rf bin/
	@go clean

deps-check:
	@go list -u -m all

migrate-up: docker-up
	@echo "Applying migrations..."
	@for f in migrations/*.up.sql; do \
	  echo "  $$f"; \
	  cat $$f | docker exec -i omnira-postgres psql -U omnira -d omnira_dev > /dev/null 2>&1 || { echo "Failed: $$f"; exit 1; }; \
	done
	@echo "Validating schema..."
	@./tools/validate-schema.sh

migrate-down: docker-up
	@echo "Rolling back migrations..."
	@for f in $$(ls -r migrations/*.down.sql); do \
	  echo "  $$f"; \
	  cat $$f | docker exec -i omnira-postgres psql -U omnira -d omnira_dev > /dev/null 2>&1 || { echo "Failed: $$f"; exit 1; }; \
	done

migrate-validate: docker-up
	@./tools/validate-schema.sh

test-isolation: docker-up migrate-up
	@./tools/test-isolation.sh

benchmark: docker-up
	@echo "Running performance benchmarks..."
	@bash ./tools/benchmark.sh

benchmark-health:
	@echo "Load testing health endpoints with k6..."
	@if command -v k6 &> /dev/null; then \
		k6 run tools/k6/health-check.js -e BASE_URL=http://localhost:8080; \
	else \
		echo "k6 not installed. Install: https://k6.io/docs/getting-started/installation/"; \
		exit 1; \
	fi

benchmark-tenancy:
	@echo "Load testing tenancy endpoints with k6..."
	@if command -v k6 &> /dev/null; then \
		k6 run tools/k6/tenancy-endpoints.js -e BASE_URL=http://localhost:8080; \
	else \
		echo "k6 not installed. Install: https://k6.io/docs/getting-started/installation/"; \
		exit 1; \
	fi
