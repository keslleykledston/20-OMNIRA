#!/bin/bash
# route.sh — AI task router para tooling/.agents do OMNIRA.
#
# Ordem: hard policy filter (determinístico, hard_filter.py) -> Jev (decision
# router, só entre candidatos já permitidos) -> log.
#
# ROUTER_MODE=shadow (default): Jev RECOMENDA, este script NUNCA executa
#   nada por conta própria. O chamador (humano ou outro agente) continua no
#   controle total; route.sh só imprime a recomendação e grava o log.
# ROUTER_MODE=active: bloqueado até .agents/router/evals/run_evals.py passar
#   os critérios de ativação (ver README.md). Hoje sempre recusa e cai em
#   shadow — não há caminho de execução automática implementado ainda.
#
# Uso:
#   .agents/router/route.sh --id TASK-123 --domains auth,migration_check \
#     --desc "revisar policy RLS da nova tabela de convites"
#
# Nunca passe secrets/credentials/tokens em --desc. hard_filter.py e
# ai_sanitize (tools/ai/lib.sh) removem padrões conhecidos, mas a defesa
# real é o chamador não incluir segredo na descrição.

set -uo pipefail
ROUTER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$ROUTER_DIR/../.." && pwd)"
source "$REPO_ROOT/tools/ai/lib.sh"

ROUTER_MODE="${ROUTER_MODE:-shadow}"
ROUTER_LOG_FILE="${ROUTER_LOG_FILE:-$ROUTER_DIR/logs/decisions.jsonl}"
mkdir -p "$(dirname "$ROUTER_LOG_FILE")"

TASK_ID=""
DOMAINS=""
DESC=""
while [ $# -gt 0 ]; do
  case "$1" in
    --id) TASK_ID="$2"; shift 2 ;;
    --domains) DOMAINS="$2"; shift 2 ;;
    --desc) DESC="$2"; shift 2 ;;
    *) echo "arg desconhecido: $1" >&2; exit 2 ;;
  esac
done

if [ -z "$TASK_ID" ] || [ -z "$DOMAINS" ] || [ -z "$DESC" ]; then
  echo "uso: route.sh --id <id> --domains <csv> --desc <texto>" >&2
  exit 2
fi

if [ "$ROUTER_MODE" = "active" ]; then
  echo "ROUTER_MODE=active bloqueado: sem evidência de evals (.agents/router/evals/run_evals.py) atendendo os critérios de ativação em README.md. Caindo para shadow." >&2
  ROUTER_MODE="shadow"
fi

# 1. Hard policy filter — determinístico, sem LLM.
FILTER_JSON=$(python3 "$ROUTER_DIR/hard_filter.py" --domains "$DOMAINS") || {
  echo "hard_filter falhou — escalando para HUMAN" >&2
  FILTER_JSON='{"forced_tier":null,"bypass_jev":true,"candidates":["HUMAN"],"reason":"hard_filter_error"}'
}

FORCED_TIER=$(printf '%s' "$FILTER_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("forced_tier") or "")')
BYPASS_JEV=$(printf '%s' "$FILTER_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("bypass_jev"))')
CANDIDATES=$(printf '%s' "$FILTER_JSON" | python3 -c 'import json,sys; print(",".join(json.load(sys.stdin).get("candidates",[])))')

# 2. Jev decide, só se não houver bypass e houver mais de 1 candidato.
JEV_TIER=""
JEV_CONFIDENCE=""
JEV_RATIONALE=""
CANDIDATE_COUNT=$(printf '%s' "$CANDIDATES" | awk -F',' '{print NF}')
if [ "$BYPASS_JEV" = "True" ] || [ -z "$CANDIDATES" ] || [ "$CANDIDATE_COUNT" -le 1 ]; then
  ROUTE_TIER="${FORCED_TIER:-$CANDIDATES}"
  JEV_RATIONALE="bypass_jev (policy determinística)"
else
  SANITIZED_DESC=$(printf '%s' "$DESC" | ai_sanitize | head -c 4000)
  JEV_SYSTEM='Você é Jev, um roteador de decisão de custo/tier para tarefas de engenharia.
Escolha APENAS entre os candidatos fornecidos — nunca invente um tier fora da lista.
Responda SOMENTE com JSON: {"tier":"<um dos candidatos>","confidence":0.0-1.0,"rationale":"curto"}.
Nunca escolha um tier mais barato só para economizar se a tarefa parecer ambígua — nesse caso baixe a confidence.'
  JEV_CONTENT=$(printf 'candidates: %s\ntask_id: %s\ndescription: %s\n' "$CANDIDATES" "$TASK_ID" "$SANITIZED_DESC")

  JEV_RAW=$(ai_json "jev-decide" "$JEV_SYSTEM" "$JEV_CONTENT" 2>/dev/null)
  if [ -z "$JEV_RAW" ]; then
    # Jev indisponível/parse falhou -> escalate, nunca downgrade, nunca assume TOOL silenciosamente.
    ROUTE_TIER=$(printf '%s' "$CANDIDATES" | tr ',' '\n' | tail -1)  # tier mais alto da lista permitida
    JEV_CONFIDENCE="0.0"
    JEV_RATIONALE="jev_unavailable -> escalated to highest allowed candidate"
  else
    JEV_TIER=$(printf '%s' "$JEV_RAW" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("tier",""))' 2>/dev/null)
    JEV_CONFIDENCE=$(printf '%s' "$JEV_RAW" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("confidence",0))' 2>/dev/null)
    JEV_RATIONALE=$(printf '%s' "$JEV_RAW" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("rationale",""))' 2>/dev/null)

    VALID_TIER=$(printf '%s' "$CANDIDATES" | tr ',' '\n' | grep -Fx "$JEV_TIER" || true)
    MIN_CONF=$(python3 -c "import yaml; print(yaml.safe_load(open('$ROUTER_DIR/policy.yaml'))['confidence']['min_threshold'])")
    LOW_CONF=$(python3 -c "print(1 if float('$JEV_CONFIDENCE' or 0) < $MIN_CONF else 0)" 2>/dev/null || echo 1)

    if [ -z "$VALID_TIER" ] || [ "$LOW_CONF" = "1" ]; then
      # tier inválido ou confidence baixa -> escala para o candidato mais alto (nunca downgrade)
      ROUTE_TIER=$(printf '%s' "$CANDIDATES" | tr ',' '\n' | tail -1)
      JEV_RATIONALE="$JEV_RATIONALE | escalated (invalid_tier_or_low_confidence)"
    else
      ROUTE_TIER="$JEV_TIER"
    fi
  fi
fi

TIMESTAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)
LOG_ENTRY=$(python3 -c '
import json, sys
print(json.dumps({
    "ts": sys.argv[1], "task_id": sys.argv[2], "router_mode": sys.argv[3],
    "domains": sys.argv[4], "candidates": sys.argv[5], "forced_tier": sys.argv[6] or None,
    "bypass_jev": sys.argv[7], "jev_tier_raw": sys.argv[8] or None,
    "jev_confidence": sys.argv[9] or None, "jev_rationale": sys.argv[10],
    "route_recommended": sys.argv[11],
    "actual_model_used": None, "tokens": None, "cost_usd": None,
    "latency_ms": None, "test_result": None
}, ensure_ascii=False))
' "$TIMESTAMP" "$TASK_ID" "$ROUTER_MODE" "$DOMAINS" "$CANDIDATES" "$FORCED_TIER" "$BYPASS_JEV" "$JEV_TIER" "$JEV_CONFIDENCE" "$JEV_RATIONALE" "$ROUTE_TIER")

echo "$LOG_ENTRY" >> "$ROUTER_LOG_FILE"

cat <<EOF
{"route_recommended":"$ROUTE_TIER","mode":"$ROUTER_MODE","candidates":"$CANDIDATES","rationale":"$JEV_RATIONALE","log":"$ROUTER_LOG_FILE"}
EOF
echo
echo "NOTE: shadow mode — esta é apenas uma recomendação. A execução real continua sob controle do chamador." >&2
echo "Para registrar o resultado real (modelo usado, tokens, custo, latência, resultado de teste), anexe uma entrada via .agents/router/log_outcome.py --task-id $TASK_ID ..." >&2
