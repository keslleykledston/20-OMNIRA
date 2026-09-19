#!/bin/bash
# triage-tests.sh — resume saída de testes (go test, integration, isolation, CI).
#
# Uso:
#   go test ./... 2>&1 | tools/ai/triage-tests.sh unit
#   tools/ai/test-isolation.sh 2>&1 | tools/ai/triage-tests.sh isolation
#
# LIMITE DE AUTORIDADE: a saída deste script é PRÉ-ANÁLISE. O veredito
# pass/fail autoritativo é o exit code do runner e o log bruto preservado.
# Nunca use este resumo como evidência final para isolamento, RLS, RBAC,
# segurança, migration destrutiva ou DR.
#
# Fallback: sem local AI, imprime as linhas FAIL/--- FAIL do log bruto.

set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
source tools/ai/lib.sh

KIND="${1:-unit}"
RAW_DIR="${LOCAL_AI_RAW_DIR:-/tmp/omnira-tests}"
mkdir -p "$RAW_DIR"
RAW_FILE="$RAW_DIR/${KIND}-$(date +%Y%m%d-%H%M%S).log"

cat > "$RAW_FILE"
LINES=$(wc -l < "$RAW_FILE")

# Veredito determinístico — não depende do modelo.
if grep -qE "^(FAIL|--- FAIL|ok.*FAIL)|^FAIL\s" "$RAW_FILE"; then
  DETERMINISTIC="fail"
elif grep -qE "^(ok|PASS|--- PASS)" "$RAW_FILE"; then
  DETERMINISTIC="pass"
else
  DETERMINISTIC="unknown"
fi

echo "raw log: $RAW_FILE ($LINES lines)"
echo "deterministic verdict: $DETERMINISTIC   <- authoritative"

[ "$DETERMINISTIC" = "pass" ] && exit 0

fallback() {
  echo "-- local AI unavailable, grep fallback --"
  grep -E "FAIL|panic:|Error:|\.go:[0-9]+" "$RAW_FILE" | head -25 \
    || echo "(no failure lines matched)"
}

SYSTEM='You triage Go test output. Reply ONLY with a JSON object:
{"category":"compile_error|assertion_failure|panic|timeout|dependency_unavailable|flaky|other",
 "probable_root_cause":"short text","relevant_errors":["verbatim lines"],
 "relevant_files":["path:line"],"recommended_next_checks":["short actionable steps"]}
Quote errors verbatim. Report only what the output shows.'

CONTENT=$(printf 'TEST_KIND: %s\nDETERMINISTIC_VERDICT: %s\n\n--- OUTPUT (tail) ---\n%s\n' \
  "$KIND" "$DETERMINISTIC" "$(tail -150 "$RAW_FILE")")

ai_json "triage-tests" "$SYSTEM" "$CONTENT" || fallback
