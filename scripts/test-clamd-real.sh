#!/usr/bin/env bash
# Proof against a REAL clamd (outbound attachments R-4, docs/ops/OUTBOUND-MEDIA.md): EICAR is found, ordinary files of several sizes are clean, a stopped clamd is an
# error (so an upload would be refused with 503 and store nothing). Throwaway clamav/clamav:1.4 container on loopback; never touches the production clamav.
# Usage: scripts/test-clamd-real.sh     (the image has its signature database baked in; the first start takes ~1-2 minutes)
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-clamd-real-$$
trap 'docker rm -fv "$NAME" >/dev/null 2>&1 || true' EXIT
docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=clamd-$$" -e CLAMAV_NO_FRESHCLAMD=true -p 127.0.0.1::3310 clamav/clamav:1.4 >/dev/null
PORT=$(docker port "$NAME" 3310/tcp | head -1 | cut -d: -f2)
echo "== waiting for clamd on 127.0.0.1:$PORT (loads its signatures)"
for i in $(seq 1 240); do
  [ "$(printf 'zPING\0' | timeout 3 bash -c "exec 3<>/dev/tcp/127.0.0.1/$PORT; cat >&3; timeout 2 head -c 4 <&3" 2>/dev/null)" = PONG ] && break
  sleep 1
done
[ "$(printf 'zPING\0' | timeout 3 bash -c "exec 3<>/dev/tcp/127.0.0.1/$PORT; cat >&3; timeout 2 head -c 4 <&3" 2>/dev/null)" = PONG ] || { echo "clamd did not come up"; docker logs "$NAME" 2>&1 | tail -15; exit 1; }
export GOFLAGS=-buildvcs=false
echo "== 1. clamd running: EICAR found, clean bytes OK, unreachable = error"
OUT=$(OMNIRA_CLAMD_ADDR="127.0.0.1:$PORT" go test -count=1 -v -run TestRealClamdFindsEICARAndFailsClosed ./internal/media/adapters 2>&1) || { echo "$OUT"; echo "FAIL: real clamd (running)"; exit 1; }
echo "$OUT" | grep -E "^(---|ok|FAIL|PASS)|EICAR is FOUND|clamav_real_test"
echo "== 2. clamd STOPPED: a scan is an error, never clean"
docker stop "$NAME" >/dev/null
OUT=$(OMNIRA_CLAMD_ADDR="127.0.0.1:$PORT" OMNIRA_CLAMD_STOPPED=1 go test -count=1 -v -run TestRealClamdFindsEICARAndFailsClosed ./internal/media/adapters 2>&1) || { echo "$OUT"; echo "FAIL: real clamd (stopped)"; exit 1; }
echo "$OUT" | grep -E "^(---|ok|FAIL|PASS)|clamav_real_test"
echo "PASS: real clamd"
