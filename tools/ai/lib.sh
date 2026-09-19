#!/bin/bash
# lib.sh — client mínimo para Local AI (llama.cpp / OpenAI-compatible).
# Fonte única de acesso ao modelo local. Nenhuma outra parte do projeto
# deve falar HTTP com o llama.cpp diretamente.

# Config (12-factor, sem hardcode de endereço/modelo/secret)
LOCAL_AI_ENABLED="${LOCAL_AI_ENABLED:-true}"
LOCAL_AI_BASE_URL="${LOCAL_AI_BASE_URL:-http://127.0.0.1:18088}"
LOCAL_AI_MODEL="${LOCAL_AI_MODEL:-hermes-3-llama-3.1-8b}"
LOCAL_AI_API_KEY="${LOCAL_AI_API_KEY:-}"
LOCAL_AI_TIMEOUT="${LOCAL_AI_TIMEOUT:-45}"
LOCAL_AI_MAX_INPUT_CHARS="${LOCAL_AI_MAX_INPUT_CHARS:-12000}"
LOCAL_AI_METRICS_FILE="${LOCAL_AI_METRICS_FILE:-.local-ai-metrics.jsonl}"

# ai_available — 0 se o modelo local está utilizável, 1 caso contrário.
ai_available() {
  [ "$LOCAL_AI_ENABLED" = "true" ] || return 1
  curl -sf -m 3 "$LOCAL_AI_BASE_URL/health" >/dev/null 2>&1
}

# ai_sanitize — remove segredos antes de enviar ao modelo.
# Nunca enviar tokens, senhas, connection strings ou chaves.
ai_sanitize() {
  sed -E \
    -e 's#(postgres|postgresql|mysql|redis|nats)://[^[:space:]"]*#\1://[REDACTED]#g' \
    -e 's#(password|passwd|secret|token|api[_-]?key|authorization|bearer)([=:"[:space:]]+)[^[:space:]",;]+#\1\2[REDACTED]#gI' \
    -e 's#(-----BEGIN[A-Z ]+PRIVATE KEY-----)[^-]*#\1[REDACTED]#g' \
    -e 's#eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]+#[REDACTED_JWT]#g'
}

# ai_metrics — registra métricas básicas. Nunca grava o prompt.
ai_metrics() {
  local tool="$1" outcome="$2" ms="$3" in_size="$4" out_size="$5"
  printf '{"ts":"%s","tool":"%s","outcome":"%s","latency_ms":%s,"input_bytes":%s,"output_bytes":%s}\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$tool" "$outcome" "$ms" "$in_size" "$out_size" \
    >> "$LOCAL_AI_METRICS_FILE" 2>/dev/null || true
}

# ai_json <tool_name> <system_prompt> <user_content>
# Chama o modelo local pedindo JSON. Em qualquer falha retorna 1 sem
# escrever nada em stdout — o chamador deve seguir com o caminho bruto.
ai_json() {
  local tool="$1" system="$2" content="$3"

  ai_available || { ai_metrics "$tool" "unavailable" 0 0 0; return 1; }

  content="$(printf '%s' "$content" | ai_sanitize | head -c "$LOCAL_AI_MAX_INPUT_CHARS")"
  local in_size=${#content}
  local start_ms=$(date +%s%3N)

  local payload
  payload=$(LAI_SYS="$system" LAI_USR="$content" LAI_MODEL="$LOCAL_AI_MODEL" python3 -c '
import json, os
print(json.dumps({
    "model": os.environ["LAI_MODEL"],
    "messages": [
        {"role": "system", "content": os.environ["LAI_SYS"]},
        {"role": "user", "content": os.environ["LAI_USR"]},
    ],
    "temperature": 0,
    "max_tokens": 700,
    "response_format": {"type": "json_object"},
}))') || { ai_metrics "$tool" "payload_error" 0 "$in_size" 0; return 1; }

  local auth=()
  [ -n "$LOCAL_AI_API_KEY" ] && auth=(-H "Authorization: Bearer $LOCAL_AI_API_KEY")

  local raw
  raw=$(curl -sf -m "$LOCAL_AI_TIMEOUT" "$LOCAL_AI_BASE_URL/v1/chat/completions" \
        -H 'Content-Type: application/json' "${auth[@]}" -d "$payload" 2>/dev/null) \
    || { ai_metrics "$tool" "request_error" $(( $(date +%s%3N) - start_ms )) "$in_size" 0; return 1; }

  local out
  out=$(printf '%s' "$raw" | python3 -c '
import json, sys
try:
    body = json.loads(sys.stdin.read())["choices"][0]["message"]["content"]
    print(json.dumps(json.loads(body), ensure_ascii=False, indent=2))
except Exception:
    sys.exit(1)
') || { ai_metrics "$tool" "parse_error" $(( $(date +%s%3N) - start_ms )) "$in_size" 0; return 1; }

  ai_metrics "$tool" "ok" $(( $(date +%s%3N) - start_ms )) "$in_size" "${#out}"
  printf '%s\n' "$out"
}
