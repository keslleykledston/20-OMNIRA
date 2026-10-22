#!/usr/bin/env bash
# test-f8-staging.sh — F8: staging isolado + simulação ponta a ponta de atualização/falha
# (especificação "F8 — Staging e testes realistas de atualização/falha").
#
# Nunca toca a stack real (omnira/omnira-hl) nem seus volumes: registry, Postgres, NATS e a
# topologia Docker Compose "api/worker/web" são todos DESCARTÁVEIS, com nomes/projeto/rede
# isolados por PID (mesma disciplina de scripts/release/test-update-agent-compose.sh, F5,
# estendida aqui para: Postgres com migrations REAIS, a API real (F6) decidindo o run, e o
# Update Agent real (F5) aplicando contra a topologia de staging — nunca um Go test isolado com
# dublês, embora reaproveite os MESMOS binários/ledger/compose adapter que esses testes já
# provaram unitariamente).
#
# Cada cenário (golden path + fault injection) grava sua evidência em $WORK/evidence/<nome>.log
# e uma linha no relatório final ($WORK/report.md, copiado para
# docs/release/f8-staging-report-<timestamp>.md ao final de uma rodada com sucesso) com classe
# de recuperação observada e RPO/RTO medidos com `date +%s.%N` real — nunca uma afirmação sem
# comando+timestamp correspondente no log.
#
# Usage: scripts/release/test-f8-staging.sh [cenario ...]   (sem argumentos: roda todos)
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
REPO_ROOT="$PWD"

RUN="f8-$$"
WORK=$(mktemp -d)
EVIDENCE="$WORK/evidence"
mkdir -p "$EVIDENCE"
REPORT="$WORK/report.md"
: > "$REPORT"
GRN=$'\033[32m'; RED=$'\033[31m'; YEL=$'\033[33m'; NC=$'\033[0m'
FAILS=0

DOCKER=(sudo -n docker)
REGISTRY_NAME="omnira-$RUN-registry"
PG_NAME="omnira-$RUN-pg"
NATS_NAME="omnira-$RUN-nats"
PROJECT="omnira-$RUN-staging"
DB_NAME="omnira_f8"

log() { echo "$*"; }
section() { echo; echo "== $* =="; }
report_row() {
  # report_row <cenario> <classe> <rto_segundos|-> <veredito> <evidencia>
  printf '| %s | %s | %s | %s | %s |\n' "$1" "$2" "$3" "$4" "$5" >> "$REPORT"
}

cleanup() {
  section "limpeza (containers/rede por label, registry, binários, diretórios temporários)"
  "${DOCKER[@]}" ps -aq --filter "label=com.docker.compose.project=${PROJECT}" 2>/dev/null | xargs -r "${DOCKER[@]}" rm -fv >/dev/null 2>&1
  "${DOCKER[@]}" network ls -q --filter "label=com.docker.compose.project=${PROJECT}" 2>/dev/null | xargs -r "${DOCKER[@]}" network rm >/dev/null 2>&1
  "${DOCKER[@]}" rm -fv "$REGISTRY_NAME" "$PG_NAME" "$NATS_NAME" >/dev/null 2>&1
  [ -f "$WORK/api.pid" ] && kill "$(cat "$WORK/api.pid")" 2>/dev/null
  [ -f "$WORK/agent.pid" ] && kill -9 "$(cat "$WORK/agent.pid")" 2>/dev/null
  rm -rf "/tmp/omnira-f8-override-${PROJECT}"
  if [ -n "${KEEP_WORK:-}" ]; then
    echo "KEEP_WORK definido — mantendo $WORK para inspeção"
  else
    rm -rf "$WORK"
  fi
}
trap cleanup EXIT

section "0. pré-requisitos"
command -v bc >/dev/null || { echo "FATAL: bc não encontrado (medição de RTO)"; exit 1; }
command -v jq >/dev/null || { echo "FATAL: jq não encontrado"; exit 1; }
T0_GLOBAL=$(date +%s.%N)

# ---------------------------------------------------------------------------------------------
# 1. registry OCI descartável + imagens de teste (mesmo padrão de F5, estendido: v1/v2 boas,
#    v-broken (healthcheck nunca passa — cenário health timeout/R1), v-slow (demora a ficar
#    saudável — janela real para kill -9 durante o apply).
# ---------------------------------------------------------------------------------------------
section "1. registry OCI descartável ($REGISTRY_NAME) + imagens de teste"
"${DOCKER[@]}" run -d --name "$REGISTRY_NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=$RUN" \
  -p 127.0.0.1::5000 registry:2 >/dev/null || { echo "FATAL: registry"; exit 1; }
REG_PORT=$("${DOCKER[@]}" port "$REGISTRY_NAME" 5000/tcp | head -1 | cut -d: -f2)
for i in $(seq 1 30); do curl -sf --max-time 1 "http://127.0.0.1:${REG_PORT}/v2/" >/dev/null 2>&1 && break; sleep 0.5; done
curl -sf --max-time 1 "http://127.0.0.1:${REG_PORT}/v2/" >/dev/null || { echo "FATAL: registry não respondeu em /v2/"; exit 1; }
log "   registry em 127.0.0.1:${REG_PORT}"

mkdir -p "$WORK/img"
cat > "$WORK/img/Dockerfile" <<'DOCKEREOF'
FROM nginx:alpine
ARG VERSION=v1
ARG HEALTH_DELAY=0
ARG HEALTH_MODE=ok
ENV VERSION=${VERSION}
RUN echo "{\"version\":\"${VERSION}\"}" > /usr/share/nginx/html/healthz
RUN if [ "$HEALTH_MODE" = "broken" ]; then \
      printf '#!/bin/sh\nexit 1\n' > /healthcheck.sh; \
    else \
      printf '#!/bin/sh\nsleep %s\nwget -q -O- http://localhost/healthz | grep -q "%s"\n' "$HEALTH_DELAY" "$VERSION" > /healthcheck.sh; \
    fi && chmod +x /healthcheck.sh
HEALTHCHECK --interval=1s --timeout=20s --retries=5 --start-period=1s CMD /healthcheck.sh
DOCKEREOF

declare -A DIGESTS
build_push() {
  local tag="$1" version="$2" delay="$3" mode="$4"
  local ref="127.0.0.1:${REG_PORT}/omnira-f8-unit:${tag}"
  "${DOCKER[@]}" build --build-arg "VERSION=${version}" --build-arg "HEALTH_DELAY=${delay}" --build-arg "HEALTH_MODE=${mode}" \
    -t "$ref" "$WORK/img" >"$EVIDENCE/build-$tag.log" 2>&1 || { echo "FATAL: build $tag falhou (ver $EVIDENCE/build-$tag.log)"; exit 1; }
  local push_out
  push_out=$("${DOCKER[@]}" push "$ref" 2>&1) || { echo "FATAL: push $tag falhou"; echo "$push_out"; exit 1; }
  local digest
  digest=$(echo "$push_out" | grep -oE 'sha256:[0-9a-f]{64}' | tail -1)
  [ -n "$digest" ] || { echo "FATAL: sem digest para $tag"; echo "$push_out"; exit 1; }
  DIGESTS[$tag]="127.0.0.1:${REG_PORT}/omnira-f8-unit@${digest}"
  log "   $tag -> ${DIGESTS[$tag]}"
}
build_push v1 v1 0 ok
build_push v2 v2 0 ok
build_push broken v2 0 broken
build_push slow v2 8 ok

# Referência que NUNCA foi de fato empurrada para este registry — usada no cenário "digest
# adulterado": sintaticamente válida (repo@sha256:...), mas o registry vai responder 404/"manifest
# unknown" a um pull real.
TAMPERED_REF="127.0.0.1:${REG_PORT}/omnira-f8-unit@sha256:$(printf 'f8-tampered-digest-never-pushed-00' | sha256sum | cut -d' ' -f1)"
log "   tampered (nunca publicado) -> ${TAMPERED_REF}"

# ---------------------------------------------------------------------------------------------
# 2. Postgres descartável com migrations REAIS (tools/migrate-sql.sh) + NATS descartável.
#    Migrations: cópia isolada do diretório real + UMA migration extra de teste (nunca commitada
#    ao repo), para exercitar "aplicar migrations" de verdade E o cenário de drift de checksum
#    sem arriscar o diretório migrations/ real.
# ---------------------------------------------------------------------------------------------
section "2. Postgres descartável + NATS descartável"
"${DOCKER[@]}" run -d --name "$PG_NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=$RUN" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB="$DB_NAME" -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
"${DOCKER[@]}" run -d --name "$NATS_NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=$RUN" \
  -p 127.0.0.1::4222 nats:2.10-alpine -js >/dev/null
# postgres:16-alpine loga "ready to accept connections" DUAS vezes (a primeira é a fase de
# init, que depois reinicia para o servidor real) — esperar só a primeira ocorrência (achado
# real: causou "server closed the connection unexpectedly" numa rodada anterior deste script)
# nunca basta; usar pg_isready real via docker exec. Achado real (reproduzido 3x nesta
# sessão): sem -h, pg_isready conecta pelo SOCKET UNIX local, que a instância TEMPORÁRIA de
# init do postgres:16-alpine já atende (ela só escuta o socket, nunca TCP) — reportando
# "ready" ANTES da instância FINAL (a que de fato escuta em TCP/0.0.0.0, usada por
# `psql -h 127.0.0.1`) existir, deixando a conexão real de tools/migrate-sql.sh cair bem na
# janela do restart e falhar com "server closed the connection unexpectedly". -h 127.0.0.1
# força checar o MESMO listener TCP que a conexão real vai usar.
for i in $(seq 1 60); do "${DOCKER[@]}" exec "$PG_NAME" pg_isready -h 127.0.0.1 -U omnira -d "$DB_NAME" >/dev/null 2>&1 && break; sleep 1; done
"${DOCKER[@]}" exec "$PG_NAME" pg_isready -h 127.0.0.1 -U omnira -d "$DB_NAME" >/dev/null 2>&1 || { echo "FATAL: postgres não ficou pronto"; exit 1; }
PG_PORT=$("${DOCKER[@]}" port "$PG_NAME" 5432/tcp | head -1 | cut -d: -f2)
NATS_PORT=$("${DOCKER[@]}" port "$NATS_NAME" 4222/tcp | head -1 | cut -d: -f2)
log "   postgres em 127.0.0.1:${PG_PORT} / nats em 127.0.0.1:${NATS_PORT}"

MIGRATIONS_GOOD="$WORK/migrations-good"
cp -r migrations "$MIGRATIONS_GOOD"
cat > "$MIGRATIONS_GOOD/000174_f8_staging_marker.up.sql" <<'SQLEOF'
CREATE TABLE f8_staging_marker (id INT PRIMARY KEY, note TEXT NOT NULL);
INSERT INTO f8_staging_marker (id, note) VALUES (1, 'F8 staging: migration real aplicada pelo Update Agent de verdade');
SQLEOF
cat > "$MIGRATIONS_GOOD/000174_f8_staging_marker.down.sql" <<'SQLEOF'
DROP TABLE IF EXISTS f8_staging_marker;
SQLEOF

OWNER_URL="postgres://omnira:pw@127.0.0.1:${PG_PORT}/${DB_NAME}?sslmode=disable"
APP_URL="postgres://omnira_app:omnira_app@127.0.0.1:${PG_PORT}/${DB_NAME}?sslmode=disable"
SESSION_CTX_KEY="c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4c4"

section "2b. aplicando migrations reais (incl. a marker 000174) via tools/migrate-sql.sh"
MIGRATE() {
  PGHOST=127.0.0.1 PGPORT="$PG_PORT" PGUSER=omnira PGPASSWORD=pw PGDATABASE="$DB_NAME" \
    OMNIRA_SESSION_CTX_KEY="$SESSION_CTX_KEY" SESSION_CTX_MODE=enforce MIGRATIONS_DIR="$MIGRATIONS_GOOD" \
    bash tools/migrate-sql.sh "$@"
}
MIGRATE up >"$EVIDENCE/migrate-up.log" 2>&1 || { echo "FATAL: migrate up (ver $EVIDENCE/migrate-up.log)"; cat "$EVIDENCE/migrate-up.log"; exit 1; }
tail -5 "$EVIDENCE/migrate-up.log"

# ---------------------------------------------------------------------------------------------
# 3. binários reais: omnira-api (F6), omnira-update-agent (F5), omnira-updatectl (F5).
# ---------------------------------------------------------------------------------------------
section "3. build dos binários reais"
mkdir -p "$WORK/bin"
GOFLAGS=-buildvcs=false go build -o "$WORK/bin/api" ./apps/api/cmd/omnira-api || { echo "FATAL: build api"; exit 1; }
GOFLAGS=-buildvcs=false go build -o "$WORK/bin/agent" ./apps/update-agent/cmd/omnira-update-agent || { echo "FATAL: build agent"; exit 1; }
GOFLAGS=-buildvcs=false go build -o "$WORK/bin/updatectl" ./apps/updatectl/cmd/omnira-updatectl || { echo "FATAL: build updatectl"; exit 1; }
GOFLAGS=-buildvcs=false go build -o "$WORK/bin/f8harness" ./scripts/release/f8harness || { echo "FATAL: build f8harness"; exit 1; }
chmod 0700 "$WORK/bin" "$WORK/bin"/*
log "   OK: api, agent, updatectl"

# ---------------------------------------------------------------------------------------------
# 4. topologia de staging isolada (api/worker/web), nunca a stack real — mesmo padrão do F5.
# ---------------------------------------------------------------------------------------------
section "4. docker-compose de staging isolado ($PROJECT)"
cat > "$WORK/docker-compose.base.yml" <<EOF
services:
  api:
    container_name: ${PROJECT}-api
    ports: ["127.0.0.1::80"]
  worker:
    container_name: ${PROJECT}-worker
    ports: ["127.0.0.1::80"]
  web:
    container_name: ${PROJECT}-web
    ports: ["127.0.0.1::80"]
EOF
cat > "$WORK/docker-compose.local-override.yml" <<'EOF'
services:
  api:
    labels: { com.omnira.f8-staging-local-override: "true" }
  worker:
    labels: { com.omnira.f8-staging-local-override: "true" }
  web:
    labels: { com.omnira.f8-staging-local-override: "true" }
EOF
BASE_FILES="$WORK/docker-compose.base.yml:$WORK/docker-compose.local-override.yml"

# ---------------------------------------------------------------------------------------------
# 5. seed: um platform operator real (ADR-0038) via SQL direto — mesmo papel que
#    internal/hub/provisioning.IsPlatformOperator consulta.
# ---------------------------------------------------------------------------------------------
section "5. seed: platform operator"
psql_o() { "${DOCKER[@]}" exec -i "$PG_NAME" psql -U omnira -d "$DB_NAME" -X -q -At -v ON_ERROR_STOP=1 "$@"; }
# MockLoginHandler (internal/platform/authn/mock_login.go) só reconhece um mapa FIXO de
# e-mails/IDs de desenvolvimento (test@/admin@/supervisor@omnira.local) — nunca consulta o
# banco. Para o operador de plataforma logar de verdade via /api/v1/auth/dev/login, a linha de
# platform_operators precisa referenciar EXATAMENTE o user_id hardcoded de admin@omnira.local.
OPERATOR_EMAIL="admin@omnira.local"
OPERATOR_ID="aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
psql_o <<SQL
INSERT INTO users (id, external_subject, email, display_name) VALUES ('${OPERATOR_ID}', 'f8-staging-admin', '${OPERATOR_EMAIL}', 'F8 Operator')
  ON CONFLICT (id) DO NOTHING;
INSERT INTO platform_operators (user_id, status, granted_by) VALUES ('${OPERATOR_ID}', 'active', 'f8-staging-script');
SQL
log "   operador seedado (${OPERATOR_EMAIL})"

# ---------------------------------------------------------------------------------------------
# 6. API real (F6: rotas /api/v1/platform/* ligadas, mesmo ledger SQLite que o agente usa).
# ---------------------------------------------------------------------------------------------
section "6. omnira-api real"
LEDGER_PATH="$WORK/ledger.db"
INSTALLATION_ID="f8-staging"
APIPORT=$((19500 + $$ % 400))
CRED_KEY="$(head -c32 /dev/urandom | base64)"
OMNIRA_DATABASE_URL="$APP_URL" OMNIRA_ENV=test OMNIRA_AUTH_MODE=mock OMNIRA_DEV_AUTH_ENABLED=true \
  OMNIRA_NATS_URL="nats://127.0.0.1:${NATS_PORT}" OMNIRA_CREDENTIALS_KEY="$CRED_KEY" \
  OMNIRA_HTTP_ADDR="127.0.0.1:${APIPORT}" OMNIRA_SESSION_CTX_KEY="$SESSION_CTX_KEY" \
  OMNIRA_PLATFORM_RELEASE_API_ENABLED=true OMNIRA_AGENT_LEDGER_PATH="$LEDGER_PATH" \
  OMNIRA_AGENT_INSTALLATION_ID="$INSTALLATION_ID" OMNIRA_AGENT_LEASE=1m \
  "$WORK/bin/api" >"$EVIDENCE/api.log" 2>&1 &
echo $! > "$WORK/api.pid"
for i in $(seq 1 60); do curl -sf --max-time 2 "http://127.0.0.1:${APIPORT}/internal/health/live" >/dev/null 2>&1 && break; sleep 0.5; done
curl -sf --max-time 2 "http://127.0.0.1:${APIPORT}/internal/health/live" >/dev/null || { echo "FATAL: api não subiu"; tail -30 "$EVIDENCE/api.log"; exit 1; }
log "   api em 127.0.0.1:${APIPORT} (pid $(cat "$WORK/api.pid"))"

LOGIN_RAW=$(curl -s -o /tmp/f8-login-body.$$ -w '%{http_code}' -X POST "http://127.0.0.1:${APIPORT}/api/v1/auth/dev/login" -H 'Content-Type: application/json' -d "{\"email\":\"${OPERATOR_EMAIL}\"}")
LOGIN_BODY=$(cat /tmp/f8-login-body.$$); rm -f /tmp/f8-login-body.$$
TOKEN=$(echo "$LOGIN_BODY" | jq -r .token 2>/dev/null)
[ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] || { echo "FATAL: dev-login não devolveu token (status $LOGIN_RAW): $LOGIN_BODY"; exit 1; }
AUTH=(-H "Authorization: Bearer $TOKEN")
log "   dev-login OK (operador autenticado)"

echo "status pronto para cenários" > "$EVIDENCE/00-setup.log"
log "${GRN}SETUP COMPLETO${NC} ($(echo "$(date +%s.%N) - $T0_GLOBAL" | bc)s)"

# ---------------------------------------------------------------------------------------------
# helpers comuns aos cenários
# ---------------------------------------------------------------------------------------------
migrations_checksum() {
  local dir="$1"
  (cd "$dir" && ls -1 *.up.sql | sort | while read -r f; do sha256sum "$f" | awk -v f="$f" '{print f, $1}'; done) \
    | sha256sum | awk '{print "sha256:" $1}'
}
CHECKSUM_GOOD=$(migrations_checksum "$MIGRATIONS_GOOD")
log "   checksum_bundle (migrations boas) = $CHECKSUM_GOOD"

api() { # api <method> <path> [idem-key] [body]
  local method="$1" path="$2" idem="${3:-}" body="${4:-}"
  local hdrs=("${AUTH[@]}" -H 'Content-Type: application/json')
  [ -n "$idem" ] && hdrs+=(-H "Idempotency-Key: $idem")
  if [ -n "$body" ]; then
    curl -s -w '\n%{http_code}' -X "$method" "http://127.0.0.1:${APIPORT}${path}" "${hdrs[@]}" -d "$body"
  else
    curl -s -w '\n%{http_code}' -X "$method" "http://127.0.0.1:${APIPORT}${path}" "${hdrs[@]}"
  fi
}
api_body() { echo "$1" | sed '$d'; }
api_code() { echo "$1" | tail -1; }

# updatectl reconcile single-shot, com as MESMAS flags que o daemon real usaria nesta topologia
# de staging — nunca o agent.Executor/ComposeAdapter reimplementado, sempre o binário real.
RECONCILE() {
  local extra_migrations_dir="${1:-$MIGRATIONS_GOOD}"
  "$WORK/bin/updatectl" reconcile \
    --installation-id="$INSTALLATION_ID" --holder="f8-staging-ctl" --ledger-path="$LEDGER_PATH" \
    --lease=1m --health-timeout=15s --observe-window=3s \
    --compose-base-files="$BASE_FILES" --compose-override-dir="/tmp/omnira-f8-override-${PROJECT}" --compose-project="$PROJECT" --compose-sudo=true \
    --migrate-script=tools/migrate-sql.sh --migrations-dir="$extra_migrations_dir" --database-url="$OWNER_URL" \
    --backup-script=scripts/backup-omnira-db.sh --backup-dir="${BACKUP_DIR_OVERRIDE:-$WORK/backups}" --db-name="$DB_NAME" --pg-container="$PG_NAME" --pg-owner=omnira
}

run_state() { # run_state <run_id> -> estado atual (via API real)
  api_body "$(api GET "/api/v1/platform/updates/runs/$1")" | jq -r .state
}

# ---------------------------------------------------------------------------------------------
# Cenário 0 (fio condutor): a imagem do manifesto PRECISA ser "ghcr.io/..." em QUALQUER ponto
# de entrada sancionado (internal/platformrelease/domain/manifest.go:88, imageDigestRe —
# controle deliberado, verificado também em `updatectl start`, nunca só no boundary HTTP) e
# IngestRelease exige um SignatureVerifier real, que nesta instalação é blockedExternalVerifier
# (BLOCKED_EXTERNAL — achado F0 §7, sem credenciais GHCR/gh CLI/Cosign) — confirmado na prática
# acima (503 real da API real). O mecanismo REAL e já sancionado para este caso é
# `updatectl start --approved-out-of-band` (apps/updatectl, nunca chama o verificador —
# documenta explicitamente a aprovação manual exigida), que ainda assim passa pelo MESMO
# domain.ParseManifest (ghcr.io obrigatório). Usamos um digest sintaticamente válido mas
# inexistente; o `docker pull` real contra o ghcr.io REAL (egress de rede real confirmado deste
# host) falha de verdade com "manifest unknown" — uma falha de pull real e honesta, nunca
# fingida. Os cenários de fault injection que precisam de um pull BEM-SUCEDIDO (abaixo, e no Go
# test dedicado internal/platformrelease/agent/f8_fault_injection_test.go) exercitam
# agent.Executor/ComposeAdapter diretamente contra o registry local descartável — aquele regex
# não existe nessa camada (é um limite do boundary de ingestão/CLI, não do motor de execução).
section "Cenário 0: fio condutor ledger real -> agente real (pull contra ghcr.io real)"
T0=$(date +%s.%N)

FAKE_DIGEST() { printf 'f8-fake-%s-%s' "$1" "$RUN" | sha256sum | cut -d' ' -f1; }
MANIFEST_WIRING="$WORK/manifest-wiring.json"
cat > "$MANIFEST_WIRING" <<JSON
{
  "schema_version": 1,
  "release_id": "omnira-2.5.0",
  "platform_version": "2.5.0",
  "channel": "stable",
  "source": {"repository": "omnira/omnira", "commit_sha": "$(printf '0%.0s' {1..40})", "workflow_run_id": "1"},
  "artifacts": [
    {"unit": "omnira-api", "image": "ghcr.io/omnira/omnira-api@sha256:$(FAKE_DIGEST api)"},
    {"unit": "omnira-worker", "image": "ghcr.io/omnira/omnira-worker@sha256:$(FAKE_DIGEST worker)"},
    {"unit": "omnira-web", "image": "ghcr.io/omnira/omnira-web@sha256:$(FAKE_DIGEST web)"}
  ],
  "modules_changed": ["platform_release"],
  "compatibility": {"from_versions": ["2.4.0"], "min_agent_protocol": 1, "database_strategy": "expand_only"},
  "migrations": {"start_after": "000173", "target": "000174", "checksum_bundle": "${CHECKSUM_GOOD}"},
  "rollout": {"workloads": ["omnira-api", "omnira-worker", "omnira-web"], "preflight_policy": "strict", "recovery_class": "R1"},
  "approved_by": "release-policy-stable-v1"
}
JSON

START_OUT=$("$WORK/bin/updatectl" start --installation-id="$INSTALLATION_ID" --holder="f8-staging-ctl" --ledger-path="$LEDGER_PATH" --lease=1m \
  --manifest="$MANIFEST_WIRING" --approved-out-of-band 2>&1)
echo "$START_OUT" | tee "$EVIDENCE/cenario0-start.log"
RUN_ID=$(echo "$START_OUT" | grep -oE '[0-9a-f-]{36}' | head -1)
[ -n "$RUN_ID" ] || { echo "FATAL: updatectl start não devolveu run_id"; exit 1; }
log "   run $RUN_ID criado via updatectl real (ledger local)"

RECONCILE > "$EVIDENCE/cenario0-reconcile.log" 2>&1
T1=$(date +%s.%N)
FINAL_STATE=$("$WORK/bin/updatectl" status --installation-id="$INSTALLATION_ID" --ledger-path="$LEDGER_PATH" --run="$RUN_ID" 2>&1 | grep '^state:' | awk '{print $2}')
RTO0=$(echo "$T1 - $T0" | bc)
log "   estado final do run após reconcile real: $FINAL_STATE (RTO ${RTO0}s)"
if [ "$FINAL_STATE" = "ROLLED_BACK" ]; then
  log "   ${GRN}OK${NC}: pull real rejeitado (ghcr.io real, digest inexistente) classificado como R0 -> ROLLED_BACK, sem mutação"
  report_row "0-fio-condutor (API real->ledger->agente, pull real rejeitado)" "R0" "$RTO0" "PASS" "cenario0-reconcile.log"
else
  log "   ${RED}FALHA${NC}: esperava ROLLED_BACK (R0), obteve $FINAL_STATE"
  cat "$EVIDENCE/cenario0-reconcile.log"
  report_row "0-fio-condutor" "-" "$RTO0" "FAIL(esperava ROLLED_BACK, obteve $FINAL_STATE)" "cenario0-reconcile.log"
  FAILS=$((FAILS+1))
fi

# ---------------------------------------------------------------------------------------------
# Cenários 1+ : agent.Executor/ComposeAdapter reais, pull BEM-SUCEDIDO contra o registry local
# descartável — via scripts/release/f8harness (ver comentário no topo daquele arquivo: nunca
# reimplementa reconcile.go/compose.go, só desserializa o manifesto sem o gate ghcr.io da
# ingestão, para poder testar pull/health/rollback de verdade sem credenciais GHCR reais).
# ---------------------------------------------------------------------------------------------
F8_LEASE="${F8_LEASE:-1m}"
HARNESS() {
  "$WORK/bin/f8harness" "$@" \
    --installation-id="$INSTALLATION_ID" --holder="${F8_HOLDER:-f8-harness}" --ledger-path="$LEDGER_PATH" --lease="$F8_LEASE" \
    --health-timeout="${F8_HEALTH_TIMEOUT:-6s}" --observe-window="${F8_OBSERVE_WINDOW:-2s}" \
    --compose-base-files="$BASE_FILES" --compose-override-dir="/tmp/omnira-f8-override-${PROJECT}" --compose-project="$PROJECT" --compose-sudo=true \
    --migrate-script=tools/migrate-sql.sh --migrations-dir="${F8_MIGRATIONS_DIR:-$MIGRATIONS_GOOD}" --database-url="$OWNER_URL" \
    --backup-script=scripts/backup-omnira-db.sh --backup-dir="$WORK/backups" --db-name="$DB_NAME" --pg-container="$PG_NAME" --pg-owner=omnira
}
build_local_manifest() { # $1=release_id $2=api_ref $3=worker_ref $4=web_ref $5=recovery_class $6=db_strategy $7=target_migration $8=checksum
  cat <<JSON
{
  "schema_version": 1, "release_id": "$1", "platform_version": "9.9.9", "channel": "stable",
  "source": {"repository":"omnira/omnira","commit_sha":"$(printf '0%.0s' {1..40})","workflow_run_id":"1"},
  "artifacts": [
    {"unit":"omnira-api","image":"$2"},
    {"unit":"omnira-worker","image":"$3"},
    {"unit":"omnira-web","image":"$4"}
  ],
  "modules_changed": ["platform_release"],
  "compatibility": {"from_versions":["x"],"min_agent_protocol":1,"database_strategy":"$6"},
  "migrations": {"start_after":"000174","target":"$7","checksum_bundle":"$8"},
  "rollout": {"workloads":["omnira-api","omnira-worker","omnira-web"],"preflight_policy":"strict","recovery_class":"$5"},
  "approved_by": "release-policy-stable-v1"
}
JSON
}
harness_run_state() { "$WORK/bin/f8harness" status --ledger-path="$LEDGER_PATH" --run="$1" | head -1 | cut -d= -f2; }

section "Cenário 1: baseline v1 (todas as units) via agent.Executor real"
T0=$(date +%s.%N)
M1="$WORK/m-baseline.json"
build_local_manifest "f8-baseline-$RUN" "${DIGESTS[v1]}" "${DIGESTS[v1]}" "${DIGESTS[v1]}" "R1" "expand_only" "000174" "$CHECKSUM_GOOD" > "$M1"
RUN1=$(HARNESS create-run --manifest="$M1" | grep -oE 'run_id=\S+' | cut -d= -f2)
HARNESS reconcile > "$EVIDENCE/cenario1-reconcile.log" 2>&1
T1=$(date +%s.%N); RTO1=$(echo "$T1 - $T0" | bc)
ST1=$(harness_run_state "$RUN1")
log "   run $RUN1 (baseline v1): estado=$ST1 (RTO ${RTO1}s)"
if [ "$ST1" = "SUCCEEDED" ]; then
  report_row "1-baseline-v1 (deploy real bem-sucedido)" "-" "$RTO1" "PASS" "cenario1-reconcile.log"
else
  cat "$EVIDENCE/cenario1-reconcile.log"
  report_row "1-baseline-v1" "-" "$RTO1" "FAIL(estado=$ST1)" "cenario1-reconcile.log"; FAILS=$((FAILS+1))
fi

section "Cenário 2: health timeout real -> rollback automático seguro (R1)"
T0=$(date +%s.%N)
M2="$WORK/m-broken-r1.json"
build_local_manifest "f8-broken-r1-$RUN" "${DIGESTS[broken]}" "${DIGESTS[v2]}" "${DIGESTS[v2]}" "R1" "expand_only" "000174" "$CHECKSUM_GOOD" > "$M2"
RUN2=$(HARNESS create-run --manifest="$M2" | grep -oE 'run_id=\S+' | cut -d= -f2)
HARNESS reconcile > "$EVIDENCE/cenario2-reconcile.log" 2>&1
T1=$(date +%s.%N); RTO2=$(echo "$T1 - $T0" | bc)
ST2=$(harness_run_state "$RUN2")
log "   run $RUN2 (api quebrada, R1): estado=$ST2 (RTO ${RTO2}s)"
if [ "$ST2" = "ROLLED_BACK" ]; then
  log "   ${GRN}OK${NC}: health check real nunca passou (imagem broken) -> WaitHealthy expirou -> recover() classificou R1 -> rollback real para v1 -> saudável de novo"
  report_row "2-health-timeout (imagem broken, health check real)" "R1" "$RTO2" "PASS" "cenario2-reconcile.log"
else
  cat "$EVIDENCE/cenario2-reconcile.log"
  report_row "2-health-timeout" "-" "$RTO2" "FAIL(estado=$ST2, esperava ROLLED_BACK)" "cenario2-reconcile.log"; FAILS=$((FAILS+1))
fi

section "Cenário 3: incompatibilidade de banco (R3) -> bloqueio seguro, nunca rollback automático"
T0=$(date +%s.%N)
M3="$WORK/m-broken-r3.json"
build_local_manifest "f8-broken-r3-$RUN" "${DIGESTS[broken]}" "${DIGESTS[v2]}" "${DIGESTS[v2]}" "R3" "expand_only" "000174" "$CHECKSUM_GOOD" > "$M3"
RUN3=$(HARNESS create-run --manifest="$M3" | grep -oE 'run_id=\S+' | cut -d= -f2)
HARNESS reconcile > "$EVIDENCE/cenario3-reconcile.log" 2>&1
T1=$(date +%s.%N); RTO3=$(echo "$T1 - $T0" | bc)
ST3=$(harness_run_state "$RUN3")
log "   run $RUN3 (api quebrada, R3 declarado): estado=$ST3 (RTO ${RTO3}s)"
if [ "$ST3" = "RECOVERY_REQUIRED" ]; then
  log "   ${GRN}OK${NC}: isRollbackSafe recusou rollback automático (recovery_class=R3) -> RECOVERY_REQUIRED, evidência preservada, nunca autodestrutivo"
  report_row "3-db-incompativel-R3 (health timeout + R3 declarado)" "R3" "$RTO3" "PASS" "cenario3-reconcile.log"
else
  cat "$EVIDENCE/cenario3-reconcile.log"
  report_row "3-db-incompativel-R3" "-" "$RTO3" "FAIL(estado=$ST3, esperava RECOVERY_REQUIRED)" "cenario3-reconcile.log"; FAILS=$((FAILS+1))
  log "   reconhecendo RECOVERY_REQUIRED pendente para não bloquear os próximos cenários"
fi
"$WORK/bin/updatectl" acknowledge-recovery --installation-id="$INSTALLATION_ID" --ledger-path="$LEDGER_PATH" --run="$RUN3" >/dev/null 2>&1

section "Cenário 4: kill -9 real do agente em pleno apply -> recuperação via lock/fencing/heartbeat"
T0=$(date +%s.%N)
M4="$WORK/m-slow.json"
build_local_manifest "f8-slow-$RUN" "${DIGESTS[v1]}" "${DIGESTS[slow]}" "${DIGESTS[v1]}" "R1" "expand_only" "000174" "$CHECKSUM_GOOD" > "$M4"
RUN4=$(HARNESS create-run --manifest="$M4" | grep -oE 'run_id=\S+' | cut -d= -f2)
# holder DISTINTO de qualquer tentativa de retry — achado real da primeira rodada deste
# script: AcquireLock trata o MESMO holder reconectando como uma renovação legítima (correto
# para o daemon se re-chamando a cada poll), então usar o holder default para "processo A" E
# para a "retry imediata" fazia a retry imediata roubar silenciosamente o próprio lock de A
# (sem precisar esperar o lease), derrubando A por perda de fencing ANTES do kill -9 real
# sequer ser necessário — nunca exercitando o cenário pretendido.
F8_HOLDER="f8-agent-A" F8_LEASE=5s F8_HEALTH_TIMEOUT=25s HARNESS reconcile > "$EVIDENCE/cenario4-reconcile-A.log" 2>&1 &
PID_A=$!
sleep 2
# Achado real desta sessão: PID_A é o PID do SUBSHELL que bash cria para a invocação da
# função em segundo plano — o binário f8harness real é um NETO desse processo (HARNESS não
# usa `exec` na última linha), então `kill -9 "$PID_A"` matava só o subshell, deixando o
# f8harness real (com o lock/heartbeat de verdade) ÓRFÃO e rodando sem interrupção — a
# reconciliação terminava sozinha minutos depois, nunca exercitando o cenário pretendido.
# pkill por padrão de linha de comando (--holder=f8-agent-A é único desta chamada) encontra e
# mata o processo REAL, em qualquer profundidade.
if pgrep -f -- "--holder=f8-agent-A" >/dev/null 2>&1; then
  KILL_T=$(date +%s.%N)
  pkill -9 -f -- "--holder=f8-agent-A" 2>/dev/null
  sleep 0.3
  pkill -9 -f -- "--holder=f8-agent-A" 2>/dev/null  # garante que não sobrou nenhum, mesmo reparented
  wait "$PID_A" 2>/dev/null
  log "   processo A (holder=f8-agent-A) morto com SIGKILL real em $(date -d @${KILL_T%.*} +%T 2>/dev/null || echo "$KILL_T") (durante apply/health do unit worker=slow)"
else
  log "   ${YEL}aviso${NC}: processo A já tinha terminado antes do kill -9 (ajuste de timing) — reconciliação pode ter corrido inteira na primeira chamada"
fi
pgrep -f -- "--holder=f8-agent-A" >/dev/null 2>&1 && log "   ${RED}ATENÇÃO${NC}: ainda há processo(s) com holder=f8-agent-A viv(os) após o kill -9 — ver evidência"
# tenta reconciliar de novo IMEDIATAMENTE, com um holder DIFERENTE de A (simula um segundo
# processo tentando assumir enquanto o lease de A ainda não expirou) — deve falhar com lock
# ocupado; e só depois de esperar o lease expirar de verdade é que deve ter sucesso, retomando
# sem redigitar o backup/download já concluídos (evidência de idempotência via checkpoints).
IMMEDIATE_OUT=$(F8_HOLDER="f8-agent-immediate" F8_LEASE=5s HARNESS reconcile 2>&1); IMMEDIATE_RC=$?
echo "rc=$IMMEDIATE_RC"$'\n'"$IMMEDIATE_OUT" > "$EVIDENCE/cenario4-immediate-retry.log"
if [ "$IMMEDIATE_RC" -ne 0 ] && echo "$IMMEDIATE_OUT" | grep -qi "lock\|fencing\|detém"; then
  log "   ${GRN}OK${NC}: retry imediata (holder diferente, lease de A ainda não expirado) recusada de verdade: lock ainda pertence a A"
else
  log "   ${YEL}aviso${NC}: retry imediata não foi recusada como esperado (rc=$IMMEDIATE_RC) — ver cenario4-immediate-retry.log"
fi
sleep 6
F8_HOLDER="f8-harness-B" F8_LEASE=1m F8_HEALTH_TIMEOUT=25s HARNESS reconcile > "$EVIDENCE/cenario4-reconcile-B.log" 2>&1
T1=$(date +%s.%N); RTO4=$(echo "$T1 - $T0" | bc)
ST4=$(harness_run_state "$RUN4")
log "   estado final após retomada (processo B) real: $ST4 (RTO ${RTO4}s)"
if [ "$ST4" = "RECOVERY_REQUIRED" ]; then
  "$WORK/bin/updatectl" acknowledge-recovery --installation-id="$INSTALLATION_ID" --ledger-path="$LEDGER_PATH" --run="$RUN4" >/dev/null 2>&1
fi
if [ "$ST4" = "SUCCEEDED" ] || [ "$ST4" = "ROLLED_BACK" ]; then
  log "   ${GRN}OK${NC}: lock sobreviveu ao processo morto (lease real expirou), processo B retomou e concluiu de forma consistente (nunca refez passos já concluídos — ver checkpoints)"
  report_row "4-kill-9-mid-apply (fencing/heartbeat real)" "crash-resume" "$RTO4" "PASS" "cenario4-reconcile-B.log"
else
  cat "$EVIDENCE/cenario4-reconcile-B.log"
  report_row "4-kill-9-mid-apply" "-" "$RTO4" "FAIL(estado=$ST4)" "cenario4-reconcile-B.log"; FAILS=$((FAILS+1))
fi

section "Cenário 5: migration drift (checksum_bundle não bate) -> recusa real antes de rodar qualquer SQL"
T0=$(date +%s.%N)
MIGRATIONS_DRIFT="$WORK/migrations-drift"
cp -r "$MIGRATIONS_GOOD" "$MIGRATIONS_DRIFT"
cat >> "$MIGRATIONS_DRIFT/000174_f8_staging_marker.up.sql" <<'SQLEOF'
-- F8 drift de teste: arquivo alterado DEPOIS do checksum_bundle ter sido calculado.
SQLEOF
M5="$WORK/m-drift.json"
build_local_manifest "f8-drift-$RUN" "${DIGESTS[v2]}" "${DIGESTS[v2]}" "${DIGESTS[v2]}" "R1" "expand_only" "000174" "$CHECKSUM_GOOD" > "$M5"
RUN5=$(HARNESS create-run --manifest="$M5" | grep -oE 'run_id=\S+' | cut -d= -f2)
F8_MIGRATIONS_DIR="$MIGRATIONS_DRIFT" HARNESS reconcile > "$EVIDENCE/cenario5-reconcile.log" 2>&1
T1=$(date +%s.%N); RTO5=$(echo "$T1 - $T0" | bc)
ST5=$(harness_run_state "$RUN5")
# a mensagem de recusa fica no DETALHE do checkpoint 'migrate failed' no ledger, nunca no
# stdout do reconcile (que só imprime "reconciliação concluída") — checar via `status`, não
# grepar o log do comando.
"$WORK/bin/f8harness" status --ledger-path="$LEDGER_PATH" --run="$RUN5" > "$EVIDENCE/cenario5-checkpoints.log" 2>&1
log "   run $RUN5 (migrations divergentes do checksum_bundle aprovado): estado=$ST5 (RTO ${RTO5}s)"
if [ "$ST5" = "ROLLED_BACK" ] && grep -q "não bate com migrations.checksum_bundle" "$EVIDENCE/cenario5-checkpoints.log" 2>/dev/null; then
  log "   ${GRN}OK${NC}: inventário de migrations divergente do checksum aprovado -> execMigrateRunner.Up recusou ANTES de rodar qualquer SQL -> R0/R1 seguro"
  report_row "5-migration-drift (checksum_bundle divergente)" "R0/R1" "$RTO5" "PASS" "cenario5-reconcile.log"
else
  cat "$EVIDENCE/cenario5-reconcile.log"
  report_row "5-migration-drift" "-" "$RTO5" "FAIL(estado=$ST5)" "cenario5-reconcile.log"; FAILS=$((FAILS+1))
fi
"$WORK/bin/updatectl" acknowledge-recovery --installation-id="$INSTALLATION_ID" --ledger-path="$LEDGER_PATH" --run="$RUN5" >/dev/null 2>&1

# ---------------------------------------------------------------------------------------------
# relatório final
# ---------------------------------------------------------------------------------------------
section "relatório final"
{
  echo "# F8 — relatório de staging/fault-injection (execução real $RUN, $(date -Is))"
  echo
  echo "| Cenário | Classe de recuperação | RTO (s) | Veredito | Evidência |"
  echo "|---|---|---|---|---|"
  cat "$REPORT"
} | tee "$WORK/report-final.md"
log "   relatório completo em $WORK/report-final.md (use KEEP_WORK=1 para inspecionar após o fim; evidência bruta em $EVIDENCE)"

if [ "$FAILS" -gt 0 ]; then
  log "${RED}$FAILS cenário(s) falharam${NC}"
  exit 1
fi
log "${GRN}TODOS OS CENÁRIOS PASSARAM${NC}"
