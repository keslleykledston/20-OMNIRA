#!/usr/bin/env bash
# test-update-agent-compose.sh — F5: exercita o Adapter Docker Compose
# (internal/platformrelease/agent/compose.go) contra Docker real:
#   1. sobe um registry OCI descartável (registry:2) e publica 2 versões de uma imagem de
#      teste minimalista cujo healthcheck só reporta "healthy" quando o conteúdo servido bate
#      com a versão esperada — prova REAL de troca de imagem, não só de "o comando retornou 0";
#   2. gera um docker-compose.yml base isolado com 3 serviços (api/worker/web), nunca o
#      docker-compose.yml real;
#   3. invoca go test com OMNIRA_COMPOSE_INTEGRATION_TEST=1 e os digests reais publicados.
# Nunca toca a stack real (omnira/omnira-hl) — project name e rede são sempre isolados
# (ADR-0054 §2.2 item 7). Tudo descartável, removido no fim mesmo em caso de falha.
#
# Usage: scripts/release/test-update-agent-compose.sh
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1

RUN="$(date -u +%Y%m%dT%H%M%SZ)-$$"
REGISTRY_NAME="omnira-test-registry-$$"
PROJECT="omnira-compose-test-$$"
WORK=$(mktemp -d)
GRN=$'\033[32m'; RED=$'\033[31m'; NC=$'\033[0m'

cleanup() {
  echo "== limpando: containers/rede do project (por label), registry, imagens locais, arquivos temporários"
  # Nunca `docker compose -p ... down` aqui: SEM `-f`, o Compose procura um docker-compose.yml
  # no diretório atual — que é a RAIZ DO REPOSITÓRIO (este script faz cd para lá no início) —
  # e usa a definição REAL do projeto (rede `omnira-network`, serviços postgres/nats/...) para
  # decidir o que remover do nosso projeto de TESTE isolado (achado Codex). A rede implícita
  # `${PROJECT}_default` que o `up` de teste criou nunca é mencionada nessa definição real,
  # então fica órfã a cada rodada. Resolver containers e redes SÓ pelos labels reais que o
  # Compose grava neles, nunca por um arquivo YAML, elimina essa ambiguidade.
  # xargs (não "$ids" citado): docker ps -aq pode devolver VÁRIOS IDs, um por linha — passá-los
  # entre aspas duplas os junta num ÚNICO argumento com newlines embutidas, que nem `docker rm`
  # nem `docker network rm` aceitam como um ID válido (bug real encontrado rodando este
  # script: o cleanup silenciosamente não removia nada com 2+ recursos).
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
# Achado Codex: ARG só existe em BUILD TIME — o HEALTHCHECK CMD roda em RUNTIME, onde "${VERSION}"
# expandia para uma string vazia (grep -q "" aceita qualquer saída não vazia), então o
# healthcheck nunca verificava a versão de verdade, só se o nginx respondia. ENV promove o
# valor para uma variável de ambiente real do container, disponível quando o CMD do
# healthcheck de fato executa.
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

echo "== 3. docker-compose.yml base isolado (sem image: fixo — o adapter sempre fornece via override)"
# container_name FIXO de propósito (não o heredoc anterior, que deixava o Compose usar seu
# padrão default <project>-<service>-1) — replica a topologia REAL (docker-compose.yml usa
# container_name: omnira-api etc.), o cenário exato que mascarou o achado Codex sobre
# resolveContainerID adivinhar nomes em vez de resolver por labels.
cat > "$WORK/docker-compose.base.yml" <<EOF
services:
  api:
    container_name: ${PROJECT}-api-fixed-name
    ports:
      - "127.0.0.1::80"
  worker:
    container_name: ${PROJECT}-worker-fixed-name
    ports:
      - "127.0.0.1::80"
  web:
    container_name: ${PROJECT}-web-fixed-name
    ports:
      - "127.0.0.1::80"
EOF

# Equivalente de teste ao docker-compose.override.yml machine-local REAL que já existe neste
# host (porta/volumes customizados) — replica o achado Codex: um `-f` explícito que não inclua
# este arquivo faria o Compose ignorá-lo silenciosamente. Um label verificável prova que o
# adapter de fato incluiu este arquivo no comando final.
# FAKE_SECRET_SENTINEL simula uma credencial real (OMNIRA_DATABASE_URL/OMNIRA_SESSION_CTX_KEY
# no host real) — prova, ao rodar DryRun contra isto, que a saída NUNCA contém o valor
# (achado Codex P1: `docker compose config` sem `--images` interpola e expõe exatamente este
# tipo de variável em texto plano).
cat > "$WORK/docker-compose.local-override.yml" <<'EOF'
services:
  api:
    labels:
      com.omnira.test-local-override: "true"
    environment:
      FAKE_SECRET_SENTINEL: "do-not-leak-this-value-12345"
  worker:
    labels:
      com.omnira.test-local-override: "true"
    environment:
      FAKE_SECRET_SENTINEL: "do-not-leak-this-value-12345"
  web:
    labels:
      com.omnira.test-local-override: "true"
    environment:
      FAKE_SECRET_SENTINEL: "do-not-leak-this-value-12345"
EOF

echo "== 4. go test (OMNIRA_COMPOSE_INTEGRATION_TEST=1)"
OMNIRA_COMPOSE_INTEGRATION_TEST=1 \
  OMNIRA_COMPOSE_TEST_IMAGE_V1="${DIGESTS[v1]}" \
  OMNIRA_COMPOSE_TEST_IMAGE_V2="${DIGESTS[v2]}" \
  OMNIRA_COMPOSE_TEST_PROJECT="$PROJECT" \
  OMNIRA_COMPOSE_TEST_BASE_FILES="$WORK/docker-compose.base.yml:$WORK/docker-compose.local-override.yml" \
  go test ./internal/platformrelease/agent/... -run TestComposeAdapter -v -count=1
rc=$?

if [ "$rc" -eq 0 ]; then
  echo "${GRN}COMPOSE ADAPTER VALIDADO CONTRA DOCKER REAL${NC}"
else
  echo "${RED}teste de integração do compose adapter falhou (rc=$rc)${NC}"
fi
exit "$rc"
