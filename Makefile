.PHONY: help build test run clean fmt vet deps-check docker-up docker-down benchmark benchmark-health benchmark-tenancy release-help release-validate-manifest update-status update-test update-test-staging

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
	@echo ""
	@echo "release / update engine (F5-F9, read-only/dry-run — nunca pulam backup/lock/revogação):"
	@echo "  release-help       - lista os alvos abaixo com exemplos de uso"
	@echo "  release-validate-manifest MANIFEST=path - valida a FORMA de um manifesto de release"
	@echo "  update-status INSTALLATION_ID=id        - status read-only do ledger local do agente"
	@echo "  update-test        - testes unit/integration reais de internal/platformrelease/*"
	@echo "  update-test-staging - suíte F8 completa (staging Docker isolado, ~10-15min)"

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

# migrate-up/migrate-down aplicam direto no omnira-postgres de dev, SEM o lock de instalação,
# o backup verificado ou a checagem de revogação que o Update Agent (apps/update-agent /
# apps/updatectl) sempre faz antes de migrar uma instalação real (F5-F9, docs/release/
# runbooks/). Seguros para reset local de banco de desenvolvimento; NUNCA chamados por
# automação de release/promoção — quem orquestra um fluxo desses define
# OMNIRA_RELEASE_FLOW=1 e o guard abaixo recusa a execução em vez de aplicar uma migração fora
# do caminho seguro. Mantidos (não apagados) porque o reset de dev continua precisando deles.
migrate-up:
	@if [ "$(OMNIRA_RELEASE_FLOW)" = "1" ]; then \
	  echo "BLOQUEADO: 'make migrate-up' nunca deve ser chamado dentro de um fluxo de release/promoção real — ele pula o lock de instalação, o backup verificado e a checagem de revogação que o Update Agent sempre faz (ver docs/release/runbooks/rollback-e-recovery.md). Use 'make update-status' / 'updatectl start' / 'updatectl reconcile'. Omita OMNIRA_RELEASE_FLOW (ou defina =0) só para reset de banco em dev local."; \
	  exit 1; \
	fi
	@$(MAKE) docker-up
	@echo "Applying migrations..."
	@for f in migrations/*.up.sql; do \
	  echo "  $$f"; \
	  cat $$f | docker exec -i omnira-postgres psql -U omnira -d omnira_dev > /dev/null 2>&1 || { echo "Failed: $$f"; exit 1; }; \
	done
	@echo "Validating schema..."
	@./tools/validate-schema.sh

migrate-down:
	@if [ "$(OMNIRA_RELEASE_FLOW)" = "1" ]; then \
	  echo "BLOQUEADO: 'make migrate-down' nunca deve ser chamado dentro de um fluxo de release/promoção real — down.sql nunca autoriza rollback de dados (ver release/policies/backup.yaml) e o Update Agent nunca o invoca. Use 'updatectl rollback' para reverter código/config (ver docs/release/runbooks/rollback-e-recovery.md). Omita OMNIRA_RELEASE_FLOW (ou defina =0) só para reset de banco em dev local."; \
	  exit 1; \
	fi
	@$(MAKE) docker-up
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

# --- Update Engine (F5-F9): alvos seguros de dry-run/status/test para o fluxo real de
# release/promote/rollback. Nenhum alvo abaixo muta uma instalação de verdade (nem o Executor
# real, nem o ledger de produção) — para operações que mutam, use diretamente apps/updatectl
# (ver docs/release/runbooks/). O procedimento completo de release/promote/rollback/recovery/
# restore/revocation está em docs/release/runbooks/.

release-help:
	@echo "Update Engine (F5-F9) — alvos seguros, nunca pulam backup/lock/revogação:"
	@echo "  release-validate-manifest MANIFEST=path/to/manifest.json"
	@echo "                        - valida só a FORMA de um manifesto (releasectl validate-manifest)"
	@echo "  update-status INSTALLATION_ID=id [LEDGER_PATH=path] [RUN=run-id]"
	@echo "                        - lista/detalha runs do ledger local (updatectl status), read-only"
	@echo "  update-test           - testes unit/integration reais de internal/platformrelease/*"
	@echo "  update-test-staging   - suíte F8 completa (staging Docker isolado, ~10-15min, requer docker)"
	@echo "  docs/release/runbooks/ tem o procedimento completo de release/promote/rollback/recovery."

release-validate-manifest:
	@test -n "$(MANIFEST)" || { echo "uso: make release-validate-manifest MANIFEST=path/to/manifest.json"; exit 2; }
	@go run ./apps/releasectl/cmd/releasectl validate-manifest --file "$(MANIFEST)"

update-status:
	@test -n "$(INSTALLATION_ID)" || { echo "uso: make update-status INSTALLATION_ID=<id> [LEDGER_PATH=...] [RUN=<run-id>]"; exit 2; }
	@go run ./apps/updatectl/cmd/omnira-updatectl status --installation-id="$(INSTALLATION_ID)" \
	  $(if $(LEDGER_PATH),--ledger-path="$(LEDGER_PATH)") $(if $(RUN),--run="$(RUN)")

update-test:
	@go test -v -race -cover ./internal/platformrelease/...

update-test-staging:
	@echo "F8: staging Docker isolado, builda 4 imagens de teste + binários reais, ~10-15min."
	@bash scripts/release/test-f8-staging.sh
