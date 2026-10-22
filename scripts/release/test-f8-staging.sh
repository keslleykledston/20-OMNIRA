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
# achado Codex: sob `set -u`, cleanup() (registrada via trap ANTES desta variável existir)
# expandiria LEDGER_PATH indefinida e abortaria a limpeza inteira se o setup falhasse antes
# da seção 6 (ex. erro de build Go, migração) — deixando registry/Postgres/NATS órfãos. Vazio
# por padrão: o `[ -f "$LEDGER_PATH" ]` em cleanup() simplesmente nunca bate.
LEDGER_PATH=""
LOCAL_IMAGES=()  # mesma razão de LEDGER_PATH="" acima — declarado cedo para cleanup() nunca
# expandir um array indefinido sob `set -u` se o setup falhar antes da seção 1 (registry).

log() { echo "$*"; }
section() { echo; echo "== $* =="; }
report_row() {
  # report_row <cenario> <classe> <rto_segundos|-> <veredito> <evidencia>
  printf '| %s | %s | %s | %s | %s |\n' "$1" "$2" "$3" "$4" "$5" >> "$REPORT"
}

cleanup() {
  section "limpeza (containers/rede por label, registry, binários, diretórios temporários)"
  # achado Codex: o trap de EXIT apagava $WORK (relatório, TODO checkpoint, o ledger) mesmo
  # numa rodada FALHA, apesar do cabeçalho do script prometer um relatório "completo" — sem
  # KEEP_WORK explícito, toda evidência de uma rodada (passou ou falhou) desaparecia assim
  # que o script terminava. Persiste relatório+evidência+ledger (nunca os binários, grandes e
  # reconstruíveis) num diretório FORA de $WORK, incondicionalmente, antes de qualquer rm -rf.
  ARTIFACTS_DIR="/tmp/omnira-f8-reports/$RUN"
  mkdir -p "$ARTIFACTS_DIR"
  [ -f "$WORK/report-final.md" ] && cp "$WORK/report-final.md" "$ARTIFACTS_DIR/"
  # achado Codex: se o script saísse ANTES da seção "relatório final" (um create-run que não
  # devolve run_id, uma interrupção) report-final.md nunca chega a existir — mas $REPORT
  # ($WORK/report.md) já tem as linhas dos cenários que JÁ terminaram, acumuladas por
  # report_row() em tempo real. Copia como fallback incondicional, nunca só o relatório final.
  [ -f "$REPORT" ] && cp "$REPORT" "$ARTIFACTS_DIR/report-partial.md"
  [ -d "$EVIDENCE" ] && cp -r "$EVIDENCE" "$ARTIFACTS_DIR/evidence"
  # achado Codex: a API real ainda tem o ledger SQLite aberto em modo WAL neste ponto — runs e
  # checkpoints recém-committed podem estar só em ledger.db-wal, nunca em ledger.db; copiar só
  # o .db (e só depois matar a API) arrisca um snapshot sem nem as tabelas. Mata a API PRIMEIRO
  # (ela fecha o SQLite ao sair) e só então copia .db junto dos sidecars -wal/-shm, se existirem.
  [ -f "$WORK/api.pid" ] && kill "$(cat "$WORK/api.pid")" 2>/dev/null && wait "$(cat "$WORK/api.pid")" 2>/dev/null
  # achado Codex: "$WORK/agent.pid" abaixo nunca é escrito por nenhum cenário real (resquício
  # de uma versão anterior) — se o script fosse interrompido (Ctrl-C/SIGTERM) durante o
  # Cenário 4, o reconciliador de fundo (f8harness/updatectl, descendente de PID_A) sobrevivia
  # à limpeza, podia continuar mutando Docker DEPOIS dos containers/ledger serem removidos, e
  # até recriar recursos. $WORK/bin/ é um caminho ÚNICO desta invocação (mktemp -d) — matar
  # por ele, nunca por holder específico de um cenário, cobre QUALQUER binário real desta
  # execução ainda vivo, em qualquer ponto do script.
  pkill -9 -f -- "$WORK/bin/" 2>/dev/null
  sleep 0.3
  pkill -9 -f -- "$WORK/bin/" 2>/dev/null
  if [ -n "$LEDGER_PATH" ] && [ -f "$LEDGER_PATH" ]; then
    cp "$LEDGER_PATH" "$ARTIFACTS_DIR/ledger.db"
    [ -f "$LEDGER_PATH-wal" ] && cp "$LEDGER_PATH-wal" "$ARTIFACTS_DIR/ledger.db-wal"
    [ -f "$LEDGER_PATH-shm" ] && cp "$LEDGER_PATH-shm" "$ARTIFACTS_DIR/ledger.db-shm"
  fi
  echo "evidência persistida em $ARTIFACTS_DIR (fora de \$WORK, sobrevive a esta limpeza)"
  "${DOCKER[@]}" ps -aq --filter "label=com.docker.compose.project=${PROJECT}" 2>/dev/null | xargs -r "${DOCKER[@]}" rm -fv >/dev/null 2>&1
  "${DOCKER[@]}" network ls -q --filter "label=com.docker.compose.project=${PROJECT}" 2>/dev/null | xargs -r "${DOCKER[@]}" network rm >/dev/null 2>&1
  "${DOCKER[@]}" rm -fv "$REGISTRY_NAME" "$PG_NAME" "$NATS_NAME" >/dev/null 2>&1
  # achado Codex: as 4 imagens de teste (tag de build + digest pós-push, ambas em
  # LOCAL_IMAGES) nunca eram removidas do Docker local compartilhado — toda rodada (passou ou
  # falhou) acumulava imagens no host, podendo esgotar disco com o tempo.
  if [ "${#LOCAL_IMAGES[@]}" -gt 0 ]; then
    "${DOCKER[@]}" rmi -f "${LOCAL_IMAGES[@]}" >/dev/null 2>&1
  fi
  rm -rf "/tmp/omnira-f8-override-${PROJECT}"
  if [ -n "${KEEP_WORK:-}" ]; then
    echo "KEEP_WORK definido — mantendo $WORK para inspeção"
  else
    # --compose-sudo=true (necessário para os comandos docker) também roda
    # scripts/backup-omnira-db.sh como root — os dumps reais dentro de $WORK/backups ficam
    # donos de root, e um `rm -rf` sem privilégio deixa esses arquivos órfãos em /tmp a cada
    # rodada. sudo -n garante a limpeza completa de verdade.
    sudo -n rm -rf "$WORK" 2>/dev/null || rm -rf "$WORK"
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
# achado Codex: imagens de teste construídas/empurradas por esta execução nunca eram
# removidas (cleanup só tira containers/rede) — ficavam acumulando no Docker local
# compartilhado deste host a cada rodada (passou ou falhou), podendo esgotar o disco. Toda
# referência (tag de build E digest pós-push) vai em LOCAL_IMAGES (declarado no topo do
# script) para `docker rmi -f` em cleanup().
build_push() {
  local tag="$1" version="$2" delay="$3" mode="$4"
  local ref="127.0.0.1:${REG_PORT}/omnira-f8-unit:${tag}"
  "${DOCKER[@]}" build --build-arg "VERSION=${version}" --build-arg "HEALTH_DELAY=${delay}" --build-arg "HEALTH_MODE=${mode}" \
    -t "$ref" "$WORK/img" >"$EVIDENCE/build-$tag.log" 2>&1 || { echo "FATAL: build $tag falhou (ver $EVIDENCE/build-$tag.log)"; exit 1; }
  LOCAL_IMAGES+=("$ref")
  local push_out
  push_out=$("${DOCKER[@]}" push "$ref" 2>&1) || { echo "FATAL: push $tag falhou"; echo "$push_out"; exit 1; }
  local digest
  digest=$(echo "$push_out" | grep -oE 'sha256:[0-9a-f]{64}' | tail -1)
  [ -n "$digest" ] || { echo "FATAL: sem digest para $tag"; echo "$push_out"; exit 1; }
  DIGESTS[$tag]="127.0.0.1:${REG_PORT}/omnira-f8-unit@${digest}"
  LOCAL_IMAGES+=("${DIGESTS[$tag]}")
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

section "2b. aplicando migrations reais SÓ até 000173 (deixa 000174 pendente para o agente real aplicar)"
MIGRATE() {
  PGHOST=127.0.0.1 PGPORT="$PG_PORT" PGUSER=omnira PGPASSWORD=pw PGDATABASE="$DB_NAME" \
    OMNIRA_SESSION_CTX_KEY="$SESSION_CTX_KEY" SESSION_CTX_MODE=enforce MIGRATIONS_DIR="$MIGRATIONS_GOOD" \
    bash tools/migrate-sql.sh "$@"
}
# achado Codex: aplicar TUDO aqui (incl. 000174) antes de qualquer run começar faz todo
# cenário exercitar só a VERIFICAÇÃO de checksum e uma aplicação no-op — uma regressão que
# pulasse aplicar migrations pendentes de verdade passaria batido. Deixa 000174 (a marker de
# teste) genuinamente PENDENTE; o Cenário 1 (baseline) é quem primeiro a aplica de verdade,
# via o agent.Executor real, e sua asserção confirma a tabela/linha reais no Postgres.
MIGRATE_TARGET=000173 MIGRATE up >"$EVIDENCE/migrate-up.log" 2>&1 || { echo "FATAL: migrate up (ver $EVIDENCE/migrate-up.log)"; cat "$EVIDENCE/migrate-up.log"; exit 1; }
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
# achado Codex: $$ % 400 pode colidir entre execuções concorrentes desta suíte (ou com
# qualquer outro serviço já na porta) — se a porta já estiver ocupada, O NOSSO processo falha
# ao abrir o socket e morre, mas o health check pode responder 200 de QUALQUER OUTRA coisa já
# escutando ali, fazendo login/instalação seguintes validarem a API ERRADA (um PASS falso, sem
# exercitar nem a API nem o ledger desta execução). RANDOM reduz a chance de colisão; mas a
# defesa real é confirmar, depois do health check passar, que o PID que NÓS lançamos está
# genuinamente vivo — só então os 200 observados podem ser atribuídos a ELE.
APIPORT=$((19500 + (RANDOM + $$) % 2000))
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
API_PID=$(cat "$WORK/api.pid")
kill -0 "$API_PID" 2>/dev/null || { echo "FATAL: health check respondeu 200 mas o PID que lançamos ($API_PID) não está vivo — porta ${APIPORT} provavelmente já ocupada por OUTRO processo, validaríamos a API errada"; tail -30 "$EVIDENCE/api.log"; exit 1; }
# achado Codex: `kill -0` só prova que o PID existe, nunca que ELE é quem de fato abriu o
# socket de escuta — se a porta já tivesse outro serviço, o health 200 podia vir dessa OUTRA
# instalação enquanto NOSSO processo ainda inicializa (DB/NATS) sem ter tentado bind ainda,
# login/GetInstallation validariam a instalação ERRADA sem nunca exercitar esta API/ledger.
# `ss -ltnp` real (via sudo, já usado pelo resto do script) mostra o PID que de fato DETÉM o
# socket LISTEN nesta porta — compara contra o PID que lançamos, nunca confia só em kill -0.
LISTEN_PID=$(sudo -n ss -ltnp 2>/dev/null | grep ":${APIPORT} " | grep -oE 'pid=[0-9]+' | head -1 | cut -d= -f2)
[ "$LISTEN_PID" = "$API_PID" ] || { echo "FATAL: o socket LISTEN em 127.0.0.1:${APIPORT} pertence ao PID $LISTEN_PID, não ao nosso ($API_PID) — porta ocupada por outro processo, validaríamos a API errada"; tail -30 "$EVIDENCE/api.log"; exit 1; }
log "   api em 127.0.0.1:${APIPORT} (pid $API_PID, confirmado vivo E dono real do socket LISTEN)"

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

# achado Codex: nenhum cenário chamava de fato a API HTTP real autenticada (api()) — exercita
# GetInstallation AGORA, antes de qualquer run existir no ledger. Depois do Cenário 1 (primeiro
# run SUCCEEDED, manifesto com imagens do registry local — nunca ghcr.io, a única forma de ter
# um pull real bem-sucedido sem credenciais GHCR) este MESMO endpoint ficaria PERMANENTEMENTE
# quebrado: GetInstallation sincroniza do ledger via latestSucceededVersion, que roda
# domain.ParseManifest no manifesto do run SUCCEEDED mais recente — e ParseManifest rejeita de
# verdade um artifacts[].image que não seja ghcr.io (confirmado nesta sessão: 500 real,
# "artifacts[].image ... não é uma referência ghcr.io"). Aqui, com o ledger ainda vazio,
# GetInstallation não tem nenhum run para sincronizar (ok=false, sem erro) — prova que a rota
# real está autenticada e funcional, mesmo não podendo provar (nesta sessão, sem credenciais
# GHCR) que ela reflete um SUCESSO real do agente — ver Pendências no IMPLEMENTATION-STATUS.md.
INST0_RESP=$(api GET /api/v1/platform/installation)
INST0_CODE=$(api_code "$INST0_RESP")
# achado Codex: um 401/403/500 real aqui só gerava um aviso no log — todos os cenários
# seguintes usam o CLI/harness, nunca a API, então uma regressão real na rota autenticada
# passaria batido e a suíte ainda terminaria em "TODOS OS CENÁRIOS PASSARAM". Vira um
# report_row real, com FAILS incrementado como qualquer outro cenário.
if [ "$INST0_CODE" = "200" ]; then
  log "   ${GRN}OK${NC}: API real (GET /api/v1/platform/installation) respondeu 200 com o ledger ainda vazio — rota autenticada e funcional"
  report_row "0b-api-real-autenticada (GetInstallation, ledger vazio)" "-" "-" "PASS" "-"
else
  log "   ${RED}FALHA${NC}: GET /api/v1/platform/installation devolveu $INST0_CODE (esperava 200): $(api_body "$INST0_RESP")"
  report_row "0b-api-real-autenticada" "-" "-" "FAIL(http=$INST0_CODE)" "-"; FAILS=$((FAILS+1))
fi

# achado Codex: scripts/backup-omnira-db.sh real tem efeitos colaterais de produção que
# --backup-dir NUNCA isola — se este host tiver EXTERNAL_MOUNT (USB) montado ou
# BACKUP_CLOUD_CONF (rclone/Google Drive) configurado de verdade, os dumps FALSOS desta
# suíte de staging seriam copiados para destinos de backup REAIS (retenção real incluída).
# sudo com env_reset descarta qualquer `export` feito ANTES do sudo (mesma razão de
# BACKUP_DIR/DB_NAME precisarem ir como argumento de `env`) — mas um wrapper que faz o
# `export` DENTRO do processo que o `env`/sudo já invocou, antes de chamar o script real,
# nunca passa pelo sudo de novo, então funciona. Nunca toca o script de produção.
BACKUP_WRAPPER="$WORK/backup-wrapper.sh"
cat > "$BACKUP_WRAPPER" <<WRAPEOF
#!/bin/sh
export EXTERNAL_MOUNT="/tmp/omnira-f8-disabled-external-mount-$RUN"
export BACKUP_CLOUD_CONF="/tmp/omnira-f8-disabled-cloud-conf-$RUN"
exec "$REPO_ROOT/scripts/backup-omnira-db.sh" "\$@"
WRAPEOF
chmod +x "$BACKUP_WRAPPER"

# updatectl reconcile single-shot, com as MESMAS flags que o daemon real usaria nesta topologia
# de staging — nunca o agent.Executor/ComposeAdapter reimplementado, sempre o binário real.
RECONCILE() {
  local extra_migrations_dir="${1:-$MIGRATIONS_GOOD}"
  "$WORK/bin/updatectl" reconcile \
    --installation-id="$INSTALLATION_ID" --holder="f8-staging-ctl" --ledger-path="$LEDGER_PATH" \
    --lease=1m --health-timeout=15s --observe-window=3s \
    --compose-base-files="$BASE_FILES" --compose-override-dir="/tmp/omnira-f8-override-${PROJECT}" --compose-project="$PROJECT" --compose-sudo=true \
    --migrate-script=tools/migrate-sql.sh --migrations-dir="$extra_migrations_dir" --database-url="$OWNER_URL" \
    --backup-script="$BACKUP_WRAPPER" --backup-dir="${BACKUP_DIR_OVERRIDE:-$WORK/backups}" --db-name="$DB_NAME" --pg-container="$PG_NAME" --pg-owner=omnira
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
CKPT0=$("$WORK/bin/updatectl" status --installation-id="$INSTALLATION_ID" --ledger-path="$LEDGER_PATH" --run="$RUN_ID" 2>&1)
echo "$CKPT0" > "$EVIDENCE/cenario0-checkpoints.log"
FINAL_STATE=$(echo "$CKPT0" | grep '^state:' | awk '{print $2}')
RTO0=$(echo "$T1 - $T0" | bc)
log "   estado final do run após reconcile real: $FINAL_STATE (RTO ${RTO0}s)"
# achado Codex: aceitar qualquer ROLLED_BACK como prova deste cenário é insuficiente — um
# backup/outra falha ANTERIOR ao pull também termina em ROLLED_BACK (R0) sem nunca ter de
# fato tentado o pull contra o ghcr.io real. Confirma o checkpoint 'download failed' real.
if [ "$FINAL_STATE" = "ROLLED_BACK" ] && echo "$CKPT0" | grep -q "download.*failed"; then
  log "   ${GRN}OK${NC}: pull real rejeitado (ghcr.io real, digest inexistente) classificado como R0 -> ROLLED_BACK, sem mutação"
  report_row "0-fio-condutor (updatectl real->ledger->agente, pull real rejeitado)" "R0" "$RTO0" "PASS" "cenario0-reconcile.log"
else
  log "   ${RED}FALHA${NC}: esperava ROLLED_BACK (R0) com checkpoint 'download failed', obteve estado=$FINAL_STATE"
  cat "$EVIDENCE/cenario0-reconcile.log"; echo "$CKPT0"
  report_row "0-fio-condutor" "-" "$RTO0" "FAIL(estado=$FINAL_STATE, sem checkpoint download-failed)" "cenario0-reconcile.log"
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
    --backup-script="$BACKUP_WRAPPER" --backup-dir="$WORK/backups" --db-name="$DB_NAME" --pg-container="$PG_NAME" --pg-owner=omnira
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
  "migrations": {"start_after":"000173","target":"$7","checksum_bundle":"$8"},
  "rollout": {"workloads":["omnira-api","omnira-worker","omnira-web"],"preflight_policy":"strict","recovery_class":"$5"},
  "approved_by": "release-policy-stable-v1"
}
JSON
}
harness_run_state() { "$WORK/bin/f8harness" status --ledger-path="$LEDGER_PATH" --run="$1" | head -1 | cut -d= -f2; }
harness_checkpoints() { "$WORK/bin/f8harness" status --ledger-path="$LEDGER_PATH" --run="$1"; }
# espera até `pattern` aparecer nos checkpoints reais do run, ou até `timeout_s` esgotar —
# usado para injetar uma falha EXATAMENTE na janela certa (ex. logo após o deploy real
# aplicar, antes do health check terminar), nunca um `sleep` fixo que pode ou acertar a
# janela ou deixar o run inteiro correr sem nenhuma falha de verdade ser exercitada.
wait_for_checkpoint() {
  local run_id="$1" pattern="$2" timeout_s="${3:-20}"
  local tries=$((timeout_s * 10 / 3))
  local i=0
  while [ "$i" -lt "$tries" ]; do
    harness_checkpoints "$run_id" 2>/dev/null | grep -q -- "$pattern" && return 0
    sleep 0.3; i=$((i+1))
  done
  return 1
}

section "Cenário 1: baseline v1 (todas as units) via agent.Executor real"
T0=$(date +%s.%N)
M1="$WORK/m-baseline.json"
RELEASE1="omnira-9.9.$$-baseline"
build_local_manifest "$RELEASE1" "${DIGESTS[v1]}" "${DIGESTS[v1]}" "${DIGESTS[v1]}" "R1" "expand_only" "000174" "$CHECKSUM_GOOD" > "$M1"
RUN1=$(HARNESS create-run --manifest="$M1" | grep -oE 'run_id=\S+' | cut -d= -f2)
HARNESS reconcile > "$EVIDENCE/cenario1-reconcile.log" 2>&1
T1=$(date +%s.%N); RTO1=$(echo "$T1 - $T0" | bc)
ST1=$(harness_run_state "$RUN1")
log "   run $RUN1 (baseline v1): estado=$ST1 (RTO ${RTO1}s)"
# achado Codex: sem isto, um regressão que pulasse aplicar a migration 000174 PENDENTE de
# verdade (ex. um bug marcando "já aplicada" sem rodar o SQL) passaria batido — confirma a
# tabela/linha REAIS no Postgres, não só que o run terminou em SUCCEEDED.
MARKER_ROW=$(psql_o -c "SELECT note FROM f8_staging_marker WHERE id=1" 2>/dev/null)
# achado Codex (tentativa revertida, documentada como limitação real — ver Pendências no
# IMPLEMENTATION-STATUS.md): tentamos aqui chamar GET /api/v1/platform/installation real após
# este run SUCCEEDED, já que GetInstallation sincroniza do ledger via
# latestSucceededVersion/applyLatestSucceededRunToInstallation "inclusive um [run] iniciado por
# updatectl" (comentário real em service.go). Corrigimos o release_id para o formato
# omnira-X.Y.Z exigido — mas latestSucceededVersion TAMBÉM roda domain.ParseManifest no
# manifesto deste run, cujos artifacts[].image são do registry LOCAL (nunca ghcr.io, a única
# forma de ter um pull real bem-sucedido sem credenciais GHCR — ver f8harness), e ParseManifest
# rejeita isso de verdade (confirmado: 500 real, "artifacts[].image ... não é uma referência
# ghcr.io"). Pior: isso deixaria GetInstallation QUEBRADA PARA SEMPRE depois do primeiro run
# f8harness bem-sucedido nesta instalação (latestSucceededVersion sempre escolhe o MAIS
# recente). Chamar a API real aqui exigiria um run SUCCEEDED com manifesto ghcr.io-válido — a
# MESMA limitação de credenciais GHCR do Cenário 0, que lá só precisa falhar (não pode haver um
# pull ghcr.io de verdade sem publicar lá), nunca suceder. Nunca fingido; revertido.
if [ "$ST1" = "SUCCEEDED" ] && [ -n "$MARKER_ROW" ]; then
  log "   ${GRN}OK${NC}: migration 000174 (pendente até aqui) aplicada de verdade pelo agente real — f8_staging_marker confirmada no Postgres: $MARKER_ROW"
  report_row "1-baseline-v1 (deploy real + migration pendente aplicada)" "-" "$RTO1" "PASS" "cenario1-reconcile.log"
else
  log "   ${RED}FALHA${NC}: f8_staging_marker ausente/vazia (marker=$MARKER_ROW)"
  cat "$EVIDENCE/cenario1-reconcile.log"
  report_row "1-baseline-v1" "-" "$RTO1" "FAIL(estado=$ST1, marker ausente)" "cenario1-reconcile.log"; FAILS=$((FAILS+1))
fi

section "Cenário 2: health timeout real -> rollback automático seguro (R1)"
T0=$(date +%s.%N)
M2="$WORK/m-broken-r1.json"
build_local_manifest "omnira-9.9.$$-broken-r1" "${DIGESTS[broken]}" "${DIGESTS[v2]}" "${DIGESTS[v2]}" "R1" "expand_only" "000174" "$CHECKSUM_GOOD" > "$M2"
RUN2=$(HARNESS create-run --manifest="$M2" | grep -oE 'run_id=\S+' | cut -d= -f2)
HARNESS reconcile > "$EVIDENCE/cenario2-reconcile.log" 2>&1
T1=$(date +%s.%N); RTO2=$(echo "$T1 - $T0" | bc)
ST2=$(harness_run_state "$RUN2")
CKPT2=$(harness_checkpoints "$RUN2"); echo "$CKPT2" > "$EVIDENCE/cenario2-checkpoints.log"
log "   run $RUN2 (api quebrada, R1): estado=$ST2 (RTO ${RTO2}s)"
# achado Codex: ROLLED_BACK sozinho não prova que a imagem broken foi de fato implantada e o
# health check real falhou nela — um backup/download falho ANTES do deploy também termina em
# ROLLED_BACK (R0), sem nunca exercitar o cenário pretendido. Confirma 'health...failed' +
# a decisão de recuperação real, e o digest do container api VOLTOU a ser v1.
# achado real: `docker ps --format '{{.Image}}'` normaliza/trunca a referência (mostrou o
# repositório SEM o "@sha256:..." numa rodada real desta sessão) — `docker inspect
# --format '{{.Config.Image}}'` preserva a referência EXATA usada para criar o container,
# igual ao que compose.go's resolveContainerID já resolve por labels (nunca nome adivinhado).
API_CID_AFTER=$("${DOCKER[@]}" ps -q --filter "label=com.docker.compose.project=${PROJECT}" --filter "label=com.docker.compose.service=api" --filter "label=com.docker.compose.oneoff=False" | head -1)
API_DIGEST_AFTER=$("${DOCKER[@]}" inspect --format '{{.Config.Image}}' "$API_CID_AFTER" 2>/dev/null)
if [ "$ST2" = "ROLLED_BACK" ] && echo "$CKPT2" | grep -q "health.*failed" && echo "$CKPT2" | grep -q "recovery_decision completed rollback" && [ "$API_DIGEST_AFTER" = "${DIGESTS[v1]}" ]; then
  log "   ${GRN}OK${NC}: health check real nunca passou (imagem broken) -> WaitHealthy expirou -> recover() classificou R1 -> rollback real para v1 (digest confirmado) -> saudável de novo"
  report_row "2-health-timeout (imagem broken, health check real)" "R1" "$RTO2" "PASS" "cenario2-reconcile.log"
else
  cat "$EVIDENCE/cenario2-reconcile.log"; echo "$CKPT2"
  log "   digest atual do unit api: $API_DIGEST_AFTER (esperava ${DIGESTS[v1]})"
  report_row "2-health-timeout" "-" "$RTO2" "FAIL(estado=$ST2, checkpoints/digest não confirmam)" "cenario2-reconcile.log"; FAILS=$((FAILS+1))
fi

section "Cenário 3: incompatibilidade de banco (R3) -> bloqueio seguro, nunca rollback automático"
T0=$(date +%s.%N)
M3="$WORK/m-broken-r3.json"
build_local_manifest "omnira-9.9.$$-broken-r3" "${DIGESTS[broken]}" "${DIGESTS[v2]}" "${DIGESTS[v2]}" "R3" "expand_only" "000174" "$CHECKSUM_GOOD" > "$M3"
RUN3=$(HARNESS create-run --manifest="$M3" | grep -oE 'run_id=\S+' | cut -d= -f2)
HARNESS reconcile > "$EVIDENCE/cenario3-reconcile.log" 2>&1
T1=$(date +%s.%N); RTO3=$(echo "$T1 - $T0" | bc)
ST3=$(harness_run_state "$RUN3")
CKPT3=$(harness_checkpoints "$RUN3"); echo "$CKPT3" > "$EVIDENCE/cenario3-checkpoints.log"
log "   run $RUN3 (api quebrada, R3 declarado): estado=$ST3 (RTO ${RTO3}s)"
# achado Codex: RECOVERY_REQUIRED sozinho não prova que o deploy da imagem broken foi
# tentado/o health check real falhou/o rollback automático foi de fato RECUSADO (uma falha de
# migração pós-snapshot também termina em RECOVERY_REQUIRED para um manifesto R3, assim como
# um rollback tentado-mas-falho). Confirma os checkpoints reais 'health...failed' e
# 'recovery_decision failed ... recovery_class=R3' e que o deploy broken ficou NO LUGAR
# (nenhuma mutação de rollback aconteceu — nunca autodestrutivo).
API_CID3=$("${DOCKER[@]}" ps -q --filter "label=com.docker.compose.project=${PROJECT}" --filter "label=com.docker.compose.service=api" --filter "label=com.docker.compose.oneoff=False" | head -1)
API_DIGEST3=$("${DOCKER[@]}" inspect --format '{{.Config.Image}}' "$API_CID3" 2>/dev/null)
if [ "$ST3" = "RECOVERY_REQUIRED" ] && echo "$CKPT3" | grep -q "health.*failed" && echo "$CKPT3" | grep -q "recovery_decision failed.*recovery_class=R3" && [ "$API_DIGEST3" = "${DIGESTS[broken]}" ]; then
  log "   ${GRN}OK${NC}: isRollbackSafe recusou rollback automático (recovery_class=R3) -> RECOVERY_REQUIRED, deploy broken permanece no lugar (digest confirmado), evidência preservada, nunca autodestrutivo"
  report_row "3-db-incompativel-R3 (health timeout + R3 declarado)" "R3" "$RTO3" "PASS" "cenario3-reconcile.log"
else
  cat "$EVIDENCE/cenario3-reconcile.log"; echo "$CKPT3"
  log "   digest atual do unit api: $API_DIGEST3 (esperava ${DIGESTS[broken]}, nunca revertido)"
  report_row "3-db-incompativel-R3" "-" "$RTO3" "FAIL(estado=$ST3, checkpoints/digest não confirmam)" "cenario3-reconcile.log"; FAILS=$((FAILS+1))
fi
"$WORK/bin/updatectl" acknowledge-recovery --installation-id="$INSTALLATION_ID" --ledger-path="$LEDGER_PATH" --run="$RUN3" >/dev/null 2>&1

# achado Codex: o Cenário 3 deixa de propósito a imagem BROKEN implantada (RECOVERY_REQUIRED,
# nunca revertida — é exatamente o ponto do cenário R3). Sem reparar isso antes do Cenário 4,
# o snapshot capturado ali seria o estado QUEBRADO — se o processo B do Cenário 4 algum dia
# tomasse o caminho de rollback (em vez de concluir o deploy novo), reverteria para a imagem
# broken, e aceitar ROLLED_BACK como PASS sem checar saúde de verdade deixaria essa falha
# passar batido. Reimplanta o baseline v1 (reaproveitando o mesmo HARNESS real) e confirma
# SUCCEEDED antes de seguir — o Cenário 4 sempre começa de um estado genuinamente saudável.
M_REPAIR="$WORK/m-repair-before-4.json"
build_local_manifest "omnira-9.9.$$-repair" "${DIGESTS[v1]}" "${DIGESTS[v1]}" "${DIGESTS[v1]}" "R1" "expand_only" "000174" "$CHECKSUM_GOOD" > "$M_REPAIR"
RUN_REPAIR=$(HARNESS create-run --manifest="$M_REPAIR" | grep -oE 'run_id=\S+' | cut -d= -f2)
HARNESS reconcile > "$EVIDENCE/repair-before-cenario4.log" 2>&1
ST_REPAIR=$(harness_run_state "$RUN_REPAIR")
[ "$ST_REPAIR" = "SUCCEEDED" ] || { echo "FATAL: reparo pré-Cenário-4 não chegou a SUCCEEDED (estado=$ST_REPAIR) — ver repair-before-cenario4.log"; cat "$EVIDENCE/repair-before-cenario4.log"; exit 1; }
log "   baseline v1 reimplantado com sucesso antes do Cenário 4 (estado anterior do Cenário 3, broken, nunca propagado)"

section "Cenário 4: kill -9 real do agente em pleno apply -> recuperação via lock/fencing/heartbeat"
T0=$(date +%s.%N)
M4="$WORK/m-slow.json"
build_local_manifest "omnira-9.9.$$-slow" "${DIGESTS[v1]}" "${DIGESTS[slow]}" "${DIGESTS[v1]}" "R1" "expand_only" "000174" "$CHECKSUM_GOOD" > "$M4"
RUN4=$(HARNESS create-run --manifest="$M4" | grep -oE 'run_id=\S+' | cut -d= -f2)
# holders ÚNICOS por invocação deste script (sufixo $RUN, já único por PID) — achado Codex:
# um literal fixo "f8-agent-A" faria duas execuções CONCORRENTES desta suíte (dois PIDs
# diferentes, projetos/ledgers isolados) se matarem uma à outra via `pkill -f`, já que o
# padrão de busca bateria no processo da OUTRA execução também.
HOLDER_A="f8-agent-A-$RUN"; HOLDER_IMMEDIATE="f8-agent-immediate-$RUN"; HOLDER_B="f8-harness-B-$RUN"
# holder DISTINTO de qualquer tentativa de retry — achado real da primeira rodada deste
# script: AcquireLock trata o MESMO holder reconectando como uma renovação legítima (correto
# para o daemon se re-chamando a cada poll), então usar o holder default para "processo A" E
# para a "retry imediata" fazia a retry imediata roubar silenciosamente o próprio lock de A
# (sem precisar esperar o lease), derrubando A por perda de fencing ANTES do kill -9 real
# sequer ser necessário — nunca exercitando o cenário pretendido.
F8_HOLDER="$HOLDER_A" F8_LEASE=5s F8_HEALTH_TIMEOUT=25s HARNESS reconcile > "$EVIDENCE/cenario4-reconcile-A.log" 2>&1 &
PID_A=$!
# espera um checkpoint REAL de que o deploy começou (nunca um `sleep` fixo — achado Codex: um
# tempo fixo pode cair ANTES de qualquer mutação real acontecer, nunca exercitando um kill
# "em pleno apply" de verdade, ou poderia deixar o run inteiro terminar sozinho antes do kill).
# 180s: o passo `migrate` real roda `tools/migrate-sql.sh up`, que verifica o checksum das
# migrations JÁ aplicadas (real, não um no-op barato) antes de confirmar que não há nada
# pendente — leva ~100s de verdade nesta topologia, confirmado nas rodadas anteriores.
# achado Codex: 'deploy started' é registrado ANTES do `docker compose up -d` real executar —
# observá-lo não prova que o Docker mutou nada; matar aqui podia interromper o agente ANTES
# de qualquer deploy real, e o cenário ainda passaria depois de um deploy normal no retry.
# 'deploy completed' só é registrado DEPOIS do Apply real retornar — ainda dentro da janela
# de health check real (a imagem "slow" leva ~8s para reportar saudável).
wait_for_checkpoint "$RUN4" "deploy completed" 180 || { echo "FATAL: run $RUN4 nunca chegou a 'deploy completed' — ver cenario4-reconcile-A.log"; cat "$EVIDENCE/cenario4-reconcile-A.log"; FAILS=$((FAILS+1)); }
# achado Codex: captura os checkpoints JÁ concluídos (backup/download/snapshot/migrate) ANTES
# do kill, para provar DEPOIS que o processo B realmente os RETOMOU sem refazer nenhum deles —
# aceitar só o estado final (SUCCEEDED/ROLLED_BACK) nunca provava a idempotência que o log
# já afirmava; uma regressão que refizesse esses passos passaria por este gate igualmente.
CKPT_BEFORE_KILL=$(harness_checkpoints "$RUN4")
echo "$CKPT_BEFORE_KILL" > "$EVIDENCE/cenario4-checkpoints-before-kill.log"
# Achado real desta sessão: PID_A é o PID do SUBSHELL que bash cria para a invocação da
# função em segundo plano — o binário f8harness real é um NETO desse processo (HARNESS não
# usa `exec` na última linha), então `kill -9 "$PID_A"` matava só o subshell, deixando o
# f8harness real (com o lock/heartbeat de verdade) ÓRFÃO e rodando sem interrupção — a
# reconciliação terminava sozinha minutos depois, nunca exercitando o cenário pretendido.
# pkill por padrão de linha de comando (--holder=$HOLDER_A é único desta chamada) encontra e
# mata o processo REAL, em qualquer profundidade — com um espaço no FINAL do padrão (achado
# Codex: $RUN="f8-$$" é derivado do PID; "f8-1234" é PREFIXO de "f8-12345", então o padrão sem
# âncora bateria em AMBOS se duas execuções desta suíte tivessem PIDs com essa relação,
# matando o reconciliador da OUTRA execução mesmo com projeto/ledger isolados — o espaço
# garante que só bate quando o valor do holder termina exatamente aí, nunca como prefixo de
# um holder mais longo).
CRASH_OK=1
if pgrep -f -- "--holder=$HOLDER_A " >/dev/null 2>&1; then
  KILL_T=$(date +%s.%N)
  pkill -9 -f -- "--holder=$HOLDER_A " 2>/dev/null
  sleep 0.3
  pkill -9 -f -- "--holder=$HOLDER_A " 2>/dev/null  # garante que não sobrou nenhum, mesmo reparented
  wait "$PID_A" 2>/dev/null
  log "   processo A (holder=$HOLDER_A) morto com SIGKILL real em $(date -d @${KILL_T%.*} +%T 2>/dev/null || echo "$KILL_T") (durante apply/health do unit worker=slow)"
else
  log "   ${RED}FALHA${NC}: processo A já tinha terminado antes do kill -9 — este cenário NÃO exercitou um crash real"
  CRASH_OK=0
fi
if pgrep -f -- "--holder=$HOLDER_A " >/dev/null 2>&1; then
  log "   ${RED}FALHA${NC}: ainda há processo(s) com holder=$HOLDER_A viv(os) após o kill -9"
  CRASH_OK=0
fi
# tenta reconciliar de novo IMEDIATAMENTE, com um holder DIFERENTE de A (simula um segundo
# processo tentando assumir enquanto o lease de A ainda não expirou) — DEVE falhar com lock
# ocupado (prova real de fencing); e só depois de esperar o lease expirar de verdade é que
# deve ter sucesso, retomando sem redigitar o backup/download já concluídos.
IMMEDIATE_OUT=$(F8_HOLDER="$HOLDER_IMMEDIATE" F8_LEASE=5s HARNESS reconcile 2>&1); IMMEDIATE_RC=$?
echo "rc=$IMMEDIATE_RC"$'\n'"$IMMEDIATE_OUT" > "$EVIDENCE/cenario4-immediate-retry.log"
if [ "$IMMEDIATE_RC" -ne 0 ] && echo "$IMMEDIATE_OUT" | grep -qi "lock\|fencing\|detém"; then
  log "   ${GRN}OK${NC}: retry imediata (holder diferente, lease de A ainda não expirado) recusada de verdade: lock ainda pertence a A"
else
  log "   ${RED}FALHA${NC}: retry imediata deveria ter sido recusada por fencing (rc=$IMMEDIATE_RC) — ver cenario4-immediate-retry.log"
  CRASH_OK=0
fi
sleep 6
F8_HOLDER="$HOLDER_B" F8_LEASE=1m F8_HEALTH_TIMEOUT=25s HARNESS reconcile > "$EVIDENCE/cenario4-reconcile-B.log" 2>&1
T1=$(date +%s.%N); RTO4=$(echo "$T1 - $T0" | bc)
ST4=$(harness_run_state "$RUN4")
CKPT_AFTER=$(harness_checkpoints "$RUN4"); echo "$CKPT_AFTER" > "$EVIDENCE/cenario4-checkpoints-after.log"
log "   estado final após retomada (processo B) real: $ST4 (RTO ${RTO4}s)"
if [ "$ST4" = "RECOVERY_REQUIRED" ]; then
  "$WORK/bin/updatectl" acknowledge-recovery --installation-id="$INSTALLATION_ID" --ledger-path="$LEDGER_PATH" --run="$RUN4" >/dev/null 2>&1
fi
# achado Codex: compara os checkpoints 'started' de backup/download/snapshot/migrate antes vs.
# depois do kill — cada um precisa aparecer o MESMO número de vezes (nunca re-executado pelo
# processo B; uma linha 'started' DUPLICADA denunciaria um passo já concluído sendo refeito).
IDEMPOTENT_OK=1
for step in backup download snapshot migrate; do
  before_n=$(echo "$CKPT_BEFORE_KILL" | grep -c "$step *started")
  after_n=$(echo "$CKPT_AFTER" | grep -c "$step *started")
  if [ "$before_n" != "$after_n" ]; then
    log "   ${RED}FALHA${NC}: checkpoint '$step started' aparecia $before_n vez(es) antes do kill e $after_n depois — passo pode ter sido refeito"
    IDEMPOTENT_OK=0
  fi
done
# achado Codex: só aceitar o estado final sem exigir que o crash/fencing REALMENTE tenham
# acontecido, E sem confirmar a idempotência real dos checkpoints, deixaria uma regressão de
# fencing ou de re-execução de passos já concluídos passar por este gate igualmente —
# CRASH_OK=1 e IDEMPOTENT_OK=1 são obrigatórios, não apenas um aviso no log.
if [ "$CRASH_OK" = 1 ] && [ "$IDEMPOTENT_OK" = 1 ] && { [ "$ST4" = "SUCCEEDED" ] || [ "$ST4" = "ROLLED_BACK" ]; }; then
  log "   ${GRN}OK${NC}: lock sobreviveu ao processo morto (lease real expirou), processo B retomou e concluiu de forma consistente (checkpoints confirmam: backup/download/snapshot/migrate nunca refeitos)"
  report_row "4-kill-9-mid-apply (fencing/heartbeat real)" "crash-resume" "$RTO4" "PASS" "cenario4-reconcile-B.log"
else
  cat "$EVIDENCE/cenario4-reconcile-B.log"
  report_row "4-kill-9-mid-apply" "-" "$RTO4" "FAIL(estado=$ST4, crash_ok=$CRASH_OK, idempotent_ok=$IDEMPOTENT_OK)" "cenario4-reconcile-B.log"; FAILS=$((FAILS+1))
fi

section "Cenário 5: migration drift (checksum_bundle não bate) -> recusa real antes de rodar qualquer SQL"
T0=$(date +%s.%N)
MIGRATIONS_DRIFT="$WORK/migrations-drift"
cp -r "$MIGRATIONS_GOOD" "$MIGRATIONS_DRIFT"
cat >> "$MIGRATIONS_DRIFT/000174_f8_staging_marker.up.sql" <<'SQLEOF'
-- F8 drift de teste: arquivo alterado DEPOIS do checksum_bundle ter sido calculado.
SQLEOF
M5="$WORK/m-drift.json"
build_local_manifest "omnira-9.9.$$-drift" "${DIGESTS[v2]}" "${DIGESTS[v2]}" "${DIGESTS[v2]}" "R1" "expand_only" "000174" "$CHECKSUM_GOOD" > "$M5"
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
log "   relatório + evidência + ledger persistidos em /tmp/omnira-f8-reports/$RUN (sobrevive à limpeza; use KEEP_WORK=1 para manter também os binários/containers)"

if [ "$FAILS" -gt 0 ]; then
  log "${RED}$FAILS cenário(s) falharam${NC}"
  exit 1
fi
log "${GRN}TODOS OS CENÁRIOS PASSARAM${NC}"
