#!/usr/bin/env bash
# Runs the presence package against a REAL, disposable Valkey (the Valkey tests skip without OMNIRA_VALKEY_TEST_URL, so the
# integration gate alone never exercises them). Proves, among others, MT-AT-025 (cache keys scoped by tenant).
set -euo pipefail
cd "$(dirname "$0")/.."
RUN=valkeytest-$$
LBL=(--label com.omnira.integration-test=true --label "com.omnira.integration-test.run=$RUN")
cleanup() { docker rm -fv "omnira-$RUN" >/dev/null 2>&1 || true; }
trap cleanup EXIT
docker run -d --name "omnira-$RUN" "${LBL[@]}" -p 127.0.0.1::6379 valkey/valkey:8-alpine >/dev/null
PORT=$(docker port "omnira-$RUN" 6379/tcp | head -1 | sed 's/.*://')
for i in $(seq 1 40); do docker exec "omnira-$RUN" valkey-cli ping 2>/dev/null | grep -q PONG && break; sleep 0.5; done
docker exec "omnira-$RUN" valkey-cli ping | grep -q PONG || { echo "valkey did not start"; exit 1; }
docker run --rm --network host -v "$PWD:/app" -w /app -e GOCACHE=/tmp/gocache -e OMNIRA_VALKEY_TEST_URL="redis://127.0.0.1:$PORT/0" golang:1.25 \
  sh -c 'git config --global --add safe.directory /app && go test -count=1 -v ./internal/presence/adapters -run Valkey' 2>&1 | grep -E "^(=== RUN|--- |PASS|FAIL|ok|SKIP)|valkey_|panic" | grep -vE "^=== RUN"
