#!/usr/bin/env bash
# PILOT.4E1: minimal ntfy-compatible sender, sourced by
# scripts/run-check-with-alert.sh. Never invoked directly by cron, never
# hardcodes ntfy.sh (NTFY_BASE_URL is configurable so a self-hosted,
# possibly-authenticated ntfy instance works without touching this file).
#
# Security invariants (Human Gate PILOT.4E1, Sections 9-12):
#   - NTFY_TOPIC is treated as secret material end to end: never logged,
#     never echoed, and never placed in any process's argv/command line.
#     The JSON publish body (which legitimately contains the topic, per
#     ntfy's own root-endpoint publish API) is fed to curl entirely via a
#     heredoc on stdin — bash expands the heredoc itself; the topic value
#     is never a literal argument to curl, jq, or any other external
#     process, so it never appears in `ps`/process-list output either.
#   - A high-entropy topic (Section 21) is generated using only characters
#     ntfy topic names accept (alphanumeric/-/_) — by construction it never
#     needs JSON escaping, so this file never shells out to build the
#     request body (no jq/python dependency on the hot path).
#   - notify_send()'s own failure (unreachable backend, non-2xx response) is
#     logged to NOTIFY_LOG with action=notification_failed and returns
#     non-zero; the caller (run-check-with-alert.sh) decides what that means
#     for the wrapper's own exit code — this file never decides that.
#   - HTTPS by default (NTFY_BASE_URL); -k/--insecure is never used; the
#     response body is never captured/logged (only the numeric HTTP status),
#     since an ntfy response can echo back message/topic metadata.
set -euo pipefail

NTFY_BASE_URL=${NTFY_BASE_URL:-https://ntfy.sh}
: "${NOTIFY_LOG:=/var/log/omnira/notification.log}"
: "${NOTIFY_CONNECT_TIMEOUT_SECONDS:=5}"
: "${NOTIFY_MAX_TIME_SECONDS:=10}"

# notify_configured: true only when a topic is actually set. Every caller
# must check this first — an unconfigured channel is a deliberate, silent
# no-op (dev/CI/disposable tests never need a real secret), never a crash
# and never a fabricated send.
notify_configured() {
  [ -n "${NTFY_TOPIC:-}" ]
}

# notify_log <check> <action> <reason> [http_status] — the single local,
# persistent record of every notification-layer decision (Section 7):
# alert_sent, reminder_sent, recovery_sent, suppressed, notification_failed.
# Never includes the topic, any bearer token, or message/customer content —
# only the sanitized reason/category string the caller already computed.
notify_log() {
  local check="$1" action="$2" reason="$3" http_status="${4:-}"
  mkdir -p "$(dirname "$NOTIFY_LOG")" 2>/dev/null || true
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) check=$check action=$action reason=$reason http_status=$http_status" >> "$NOTIFY_LOG" 2>/dev/null || true
}

# _json_escape <string> — minimal, dependency-free JSON string escaping for
# the fields THIS file builds itself (title/message: sanitized operational
# text, never secret material). Pure bash parameter substitution — spawns no
# external process, so nothing here can leak into another process's argv.
_json_escape() {
  local s="$1"
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=${s//$'\t'/\\t}
  s=${s//$'\r'/}
  s=${s//$'\n'/\\n}
  printf '%s' "$s"
}

# notify_send <check> <kind> <priority> <title> <message>
#   kind: ALERT | REMINDER | RECOVERY | TEST — used only for the ntfy "tags"
#   field and this file's own logging; carries no authentication weight.
#   priority: ntfy priority 1-5.
# Publishes to the ROOT of NTFY_BASE_URL (never .../$NTFY_TOPIC — Section 11)
# with a JSON body that names the topic, delivered to curl purely via a
# heredoc on stdin. Returns 0 on a 2xx response, non-zero otherwise; a
# non-zero return is always accompanied by a notification_failed log line.
# On return 0, NOTIFY_LAST_RESULT is "sent" (delivered, 2xx) or "skipped"
# (channel not configured, nothing was attempted); callers must log the
# latter as notification_skipped, never as a send.
notify_send() {
  local check="$1" kind="$2" priority="$3" title="$4" message="$5"
  if ! notify_configured; then
    NOTIFY_LAST_RESULT=skipped
    return 0
  fi
  NOTIFY_LAST_RESULT=sent

  local tag
  case "$kind" in
    ALERT) tag="rotating_light" ;;
    REMINDER) tag="hourglass" ;;
    RECOVERY) tag="white_check_mark" ;;
    TEST) tag="test_tube" ;;
    *) tag="information_source" ;;
  esac

  local esc_title esc_message
  esc_title=$(_json_escape "$title")
  esc_message=$(_json_escape "$message")

  # The token travels in a 0600 header file (curl -H @file), never in curl's
  # argv where `ps` would show it. printf is a builtin: no process sees it.
  local auth_args=() hdr_file=""
  if [ -n "${NTFY_TOKEN:-}" ]; then
    hdr_file=$(mktemp) || { notify_log "$check" "notification_failed" "token_header_unavailable"; return 1; }
    printf 'Authorization: Bearer %s\n' "$NTFY_TOKEN" > "$hdr_file"
    auth_args=(-H "@${hdr_file}")
  fi

  local http_status curl_rc=0
  http_status=$(curl -sS \
    --connect-timeout "$NOTIFY_CONNECT_TIMEOUT_SECONDS" \
    --max-time "$NOTIFY_MAX_TIME_SECONDS" \
    -o /dev/null -w '%{http_code}' \
    -H "Content-Type: application/json" \
    "${auth_args[@]}" \
    --data-binary @- \
    "$NTFY_BASE_URL" 2>/dev/null <<JSONEOF
{"topic":"${NTFY_TOPIC}","title":"${esc_title}","message":"${esc_message}","priority":${priority},"tags":["${tag}"]}
JSONEOF
  ) || curl_rc=$?
  if [ -n "$hdr_file" ]; then rm -f "$hdr_file"; fi

  if [ "$curl_rc" -ne 0 ]; then
    notify_log "$check" "notification_failed" "curl_exit_${curl_rc}"
    return 1
  fi

  case "$http_status" in
    2??) return 0 ;;
    *)
      notify_log "$check" "notification_failed" "http_${http_status}" "$http_status"
      return 1
      ;;
  esac
}
