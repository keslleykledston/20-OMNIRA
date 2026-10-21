#!/usr/bin/env bash
# test-update-agent-cli.sh — F5: exercita apps/updatectl ponta a ponta (start -> reconcile ->
# upgrade -> rollback) contra Docker real, reaproveitando a MESMA infraestrutura de registry +
# imagens v1/v2 + compose files isolados de scripts/release/test-update-agent-compose.sh.
# Diferença chave: este script valida o CLI REAL (apps/updatectl/cmd/omnira-updatectl), não só
# o Compose Adapter em isolamento — prova que agentconfig.Build() monta Migrate/Backup/Compose
# corretamente e que o fluxo completo (ledger local -> Docker real) funciona de ponta a ponta.
# Nunca toca a stack real (omnira/omnira-hl) — project name e rede são sempre isolados
# (ADR-0054 §2.2 item 7). Tudo descartável, removido no fim mesmo em caso de falha.
#
# Usage: scripts/release/test-update-agent-cli.sh
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1

RUN="$(date -u +%Y%m%dT%H%M%SZ)-$$"
REGISTRY_NAME="omnira-test-registry-cli-$$"
PROJECT="omnira-cli-test-$$"
WORK=$(mktemp -d)
GRN=$'\033[32m'; RED=$'\033[31m'; NC=$'\033[0m'

cleanup() {
  echo "== limpando: containers/rede do project (por label), registry, imagens locais, arquivos temporários"
  sudo -n docker ps -aq --filter "label=com.docker.compose.project=${PROJECT}" | xargs -r sudo -n docker rm -fv >/dev/null 2>&1
  sudo -n docker network ls -q --filter "label=com.docker.compose.project=${PROJECT}" | xargs -r sudo -n docker network rm >/dev/null 2>&1
  for v in v1 v2; do
    sudo -n docker rmi "127.0.0.1:${REG_PORT:-0}/omnira-test-unit:${v}" >/dev/null 2>&1
  done
  sudo -n docker rm -fv "$REGISTRY_NAME" >/dev/null 2>&1
  rm -rf "$WORK" "/tmp/omnira-compose-adapter-test-override-${PROJECT}"
}
trap cleanup EXIT

echo "== 1. registry OCI descartável ($REGISTRY_NAME)"
sudo -n docker run -d --name "$REGISTRY_NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=$RUN" \
  -p 127.0.0.1::5000 registry:2 >/dev/null || { echo "FATAL: não foi possível iniciar o registry"; exit 1; }
REG_PORT=$(sudo -n docker port "$REGISTRY_NAME" 5000/tcp | head -1 | cut -d: -f2)
echo "   registry em 127.0.0.1:${REG_PORT}"

echo "== 2. imagem de teste (nginx:alpine + healthcheck sensível à versão)"
mkdir -p "$WORK/img"
cat > "$WORK/img/Dockerfile" <<'EOF'
FROM nginx:alpine
ARG VERSION=v1
ENV VERSION=${VERSION}
RUN echo "{\"version\":\"${VERSION}\"}" > /usr/share/nginx/html/healthz
HEALTHCHECK --interval=1s --timeout=2s --retries=5 --start-period=1s CMD wget -q -O- http://localhost/healthz | grep -q "${VERSION}"
EOF

declare -A DIGESTS
for v in v1 v2; do
  ref="127.0.0.1:${REG_PORT}/omnira-test-unit:${v}"
  sudo -n docker build --build-arg "VERSION=${v}" -t "$ref" "$WORK/img" >/dev/null 2>&1 || { echo "FATAL: build de $v falhou"; exit 1; }
  push_out=$(sudo -n docker push "$ref" 2>&1) || { echo "FATAL: push de $v falhou"; echo "$push_out"; exit 1; }
  digest=$(echo "$push_out" | grep -oE 'sha256:[0-9a-f]{64}' | tail -1)
  [ -n "$digest" ] || { echo "FATAL: não consegui extrair o digest de $v"; echo "$push_out"; exit 1; }
  DIGESTS[$v]="127.0.0.1:${REG_PORT}/omnira-test-unit@${digest}"
  echo "   $v -> ${DIGESTS[$v]}"
done

echo "== 3. docker-compose.yml base isolado (só 'api' — o manifesto de teste só declara essa unit)"
cat > "$WORK/docker-compose.base.yml" <<EOF
services:
  api:
    container_name: ${PROJECT}-api-fixed-name
    ports:
      - "127.0.0.1::80"
EOF
cat > "$WORK/docker-compose.local-override.yml" <<'EOF'
services:
  api:
    labels:
      com.omnira.test-local-override: "true"
EOF

echo "== 4. go test (OMNIRA_COMPOSE_INTEGRATION_TEST=1) — apps/updatectl ponta a ponta"
OMNIRA_COMPOSE_INTEGRATION_TEST=1 \
  OMNIRA_COMPOSE_TEST_IMAGE_V1="${DIGESTS[v1]}" \
  OMNIRA_COMPOSE_TEST_IMAGE_V2="${DIGESTS[v2]}" \
  OMNIRA_COMPOSE_TEST_PROJECT="$PROJECT" \
  OMNIRA_COMPOSE_TEST_BASE_FILES="$WORK/docker-compose.base.yml:$WORK/docker-compose.local-override.yml" \
  go test ./apps/updatectl/... -run TestUpdatectl_Integration -v -count=1
rc=$?

if [ "$rc" -eq 0 ]; then
  echo "${GRN}UPDATECTL VALIDADO PONTA A PONTA CONTRA DOCKER REAL${NC}"
else
  echo "${RED}teste de integração do updatectl falhou (rc=$rc)${NC}"
fi
exit "$rc"
