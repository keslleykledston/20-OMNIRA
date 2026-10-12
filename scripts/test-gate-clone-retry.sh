#!/usr/bin/env bash
# Proof of the clone retry of the integration gate (scripts/lib/clone-retry.sh) without a real cluster: psql_owner and docker are replaced by fakes that fail the way
# the disposable Postgres does. Usage: scripts/test-gate-clone-retry.sh
set -uo pipefail
cd "$(dirname "$0")/.."
source scripts/lib/clone-retry.sh
export CLONE_WAIT_SECONDS=1 CONTAINER=fake PG_USER=omnira
pass=0; fail=0
check() { if [ "$2" = "$3" ]; then echo "PASS: $1"; pass=$((pass+1)); else echo "FAIL: $1 (got '$2', want '$3')"; fail=$((fail+1)); fi; }
docker() { return 0; }   # pg_isready answers at once
DROPPED='psql: error: connection to server at "127.0.0.1", port 5432 failed: server closed the connection unexpectedly'
OUT=$(mktemp); CNT=$(mktemp -d); trap 'rm -rf "$OUT" "$CNT"' EXIT
bump() { echo x >> "$CNT/$1"; }            # psql_owner runs in a subshell: counters live in files
count() { [ -f "$CNT/$1" ] && wc -l < "$CNT/$1" | tr -d ' ' || echo 0; }
reset() { rm -f "$CNT"/*; }
run() { clone_template_with_retry "$@" >"$OUT" 2>&1; rc=$?; }   # no command substitution: the counters must survive in this shell

# 1. two dropped connections, then success: cloned, three CREATEs
reset
psql_owner() { case "$*" in *"CREATE DATABASE"*) bump creates; [ "$(count creates)" -le 2 ] && { echo "$DROPPED" >&2; return 1; }; return 0 ;; *) return 0 ;; esac; }
run db1 tpl pkg1
check "two dropped connections then success => cloned" "$rc" "0"
check "...after exactly three CREATE attempts" "$(count creates)" "3"
check "...and the retries were announced" "$(grep -c 'dropped connection' "$OUT")" "2"

# 2. a real failure (not the infrastructure) stops at once and says what it is
reset
psql_owner() { case "$*" in *"CREATE DATABASE"*) bump creates; echo 'ERROR:  permission denied to create database' >&2; return 1 ;; *) return 0 ;; esac; }
run db2 tpl pkg2
check "a permission error is not retried" "$(count creates)" "1"
check "...and fails" "$rc" "1"
check "...with its own message" "$(grep -c 'FATAL: cloning the template for pkg2 failed: ERROR:  permission denied' "$OUT")" "1"

# 3. the infrastructure never comes back: five attempts, then failure
reset
psql_owner() { case "$*" in *"CREATE DATABASE"*) bump creates; echo "$DROPPED" >&2; return 1 ;; *) return 0 ;; esac; }
run db3 tpl pkg3
check "a cluster that never comes back is tried 5 times" "$(count creates)" "5"
check "...and fails" "$rc" "1"

# 4. CLONE_ATTEMPTS is honoured
reset
CLONE_ATTEMPTS=2 run db4 tpl pkg4
check "CLONE_ATTEMPTS=2 stops after two" "$(count creates)" "2"

# 5. the leftovers of a failed attempt are dropped before the next one
reset
psql_owner() { case "$*" in *"CREATE DATABASE"*) bump creates; [ "$(count creates)" -le 1 ] && { echo "$DROPPED" >&2; return 1; }; return 0 ;; *"DROP DATABASE IF EXISTS db5"*) bump dropped; return 0 ;; *) return 0 ;; esac; }
run db5 tpl pkg5
check "the half-made database of a dropped attempt is removed" "$(count dropped)" "1"

echo "$pass passed, $fail failed"; [ "$fail" = 0 ]
