#!/bin/bash
# triage-logs.sh — reduz logs extensos a um diagnóstico curto e estruturado.
#
# Uso:
#   docker compose logs otel-collector 2>&1 | tools/ai/triage-logs.sh otel
#   tools/ai/triage-logs.sh postgres < /tmp/pg.log
#
# O log bruto NUNCA é descartado: é preservado em $LOCAL_AI_RAW_DIR e o
# caminho aparece na saída para consulta quando o resumo não bastar.
#
# Fallback: com LOCAL_AI_ENABLED=false ou llama.cpp offline, imprime as
# linhas de erro mais relevantes via grep e sai com 0.

set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
source tools/ai/lib.sh

SOURCE="${1:-unknown}"
RAW_DIR="${LOCAL_AI_RAW_DIR:-/tmp/omnira-logs}"
mkdir -p "$RAW_DIR"
RAW_FILE="$RAW_DIR/${SOURCE}-$(date +%Y%m%d-%H%M%S).log"

cat > "$RAW_FILE"
LINES=$(wc -l < "$RAW_FILE")

echo "raw log: $RAW_FILE ($LINES lines)"

fallback() {
  echo "-- local AI unavailable, grep fallback --"
  grep -iE "error|fatal|panic|failed|refused|denied|timeout" "$RAW_FILE" \
    | tail -20 || echo "(no error lines matched)"
}

SYSTEM='You triage infrastructure logs. Reply ONLY with a JSON object:
{"status":"healthy|degraded|failing","summary":"one sentence",
 "probable_root_cause":"short text or null","relevant_errors":["verbatim log lines"],
 "recommended_next_checks":["short actionable steps"]}
Rules:
- relevant_errors holds ONLY lines showing an error, warning or failure.
  Lines at level info/debug are never errors. If there are none, use [].
- Quote lines verbatim. Never invent a cause the log does not show.
- If status is healthy, probable_root_cause is null and relevant_errors is [].'

# Cabeça + cauda: os erros de startup ficam no topo, o estado atual no fim.
CONTENT=$(printf 'SOURCE: %s\nTOTAL_LINES: %s\n\n--- HEAD ---\n%s\n\n--- TAIL ---\n%s\n' \
  "$SOURCE" "$LINES" "$(head -60 "$RAW_FILE")" "$(tail -80 "$RAW_FILE")")

ai_json "triage-logs" "$SYSTEM" "$CONTENT" || fallback
