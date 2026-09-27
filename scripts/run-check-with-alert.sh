#!/usr/bin/env bash
# PILOT.4E1: stateful, deduplicated external-notification wrapper around an
# EXISTING health check. This script never reimplements health semantics —
# scripts/nats-jetstream-check.sh and scripts/waha-session-check.sh remain
# the sole authority for what OK/WARN/CRITICAL/FAIL mean. It runs the given
# check command exactly once per invocation and classifies THIS run's own
# captured stdout/stderr and exit code — never a shared/stale log file,
# which a concurrent or previous run could have written.
#
# Severity classification (grounded primarily in the check's own exit code,
# which is a well-defined, non-ambiguous signal; text is only needed to
# disambiguate the two states both scripts report via exit 0):
#   exit 3            -> CRITICAL   (nats-jetstream-check.sh only)
#   exit 1            -> FAIL       (both scripts)
#   exit 0, last line's 2nd field == WARN -> WARN
#   exit 0, otherwise -> OK
#
# Alerting-state model (Human Gate PILOT.4E1 Section 3) — separate from the
# check's own OK/WARN/CRITICAL/FAIL vocabulary:
#   healthy (OK) / degraded (WARN) -> failing (FAIL/CRITICAL):
#       send ALERT once.
#   failing -> failing, SAME severity:
#       suppress until NOTIFY_REMINDER_SECONDS have elapsed since the last
#       SUCCESSFUL notification, then send one REMINDER.
#   failing -> failing, DIFFERENT severity (e.g. FAIL <-> CRITICAL):
#       treated as materially changed -> send one ALERT now, reset the
#       reminder clock (never storms on every reason-string change, only on
#       an actual severity-class change).
#   failing -> OK or failing -> WARN:
#       send RECOVERY once (a failing->WARN recovery message says so
#       explicitly: the alerting condition cleared, but the check is still
#       WARN locally).
#   healthy/degraded -> healthy/degraded (OK<->WARN either direction):
#       silent. This is what stops a local 70% WARN from ever becoming an
#       external notification storm.
#
# A notification SEND failing (Section 8) never changes what this wrapper
# reports about the check itself: if the check failed, this wrapper's exit
# code is always the check's own non-zero code, regardless of notify
# success/failure. If the check succeeded (exit 0) but a due ALERT/RECOVERY
# could not be sent, this wrapper exits 10 (a distinct, documented
# notification-layer failure code) instead of silently reporting 0 — see
# Section 8. The dedup state's LAST_NOTIFIED_AT is only advanced on an
# ACTUAL successful send, so a failed send is retried on the very next
# invocation rather than waiting out the reminder interval (Section 20).
#
# Usage:
#   run-check-with-alert.sh <check-name> <command> [args...]
#   run-check-with-alert.sh --synthetic-send <check-name> <ALERT|RECOVERY|REMINDER|TEST> <message>
#
# --synthetic-send exercises ONLY scripts/lib/notify.sh's real send path
# (Section 12/15: prove delivery reaches the channel) — it runs no check
# command and never touches the dedup state file. The full detection +
# dedup + state-transition pipeline (Sections 15-19) is proven by pointing a
# REAL check command at a disposable/invalid endpoint — see
# scripts/test-notify-wrapper.sh — never by faking state here.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/notify.sh
source "$SCRIPT_DIR/lib/notify.sh"

# The topic (and any future bearer token) lives outside the repo entirely.
# Sourcing is a no-op when the file doesn't exist (dev/CI/disposable tests),
# so NTFY_TOPIC simply stays unset and notify_configured() reports false —
# never an error, never a fabricated send.
NOTIFY_ENV_FILE=${NOTIFY_ENV_FILE:-/etc/omnira/notify.env}
if [ -r "$NOTIFY_ENV_FILE" ]; then
  set -a
  # shellcheck source=/dev/null
  source "$NOTIFY_ENV_FILE"
  set +a
fi

NOTIFY_STATE_DIR=${NOTIFY_STATE_DIR:-/var/lib/omnira/notify-state}
NOTIFY_REMINDER_SECONDS=${NOTIFY_REMINDER_SECONDS:-3600}
NOTIFY_LOCK_TIMEOUT_SECONDS=${NOTIFY_LOCK_TIMEOUT_SECONDS:-30}

# NOTIFY_TEST_NOW_EPOCH: test-only clock injection (Section 17). Production
# invocations (cron) must never set this — when unset, "now" is always the
# real wall clock. Exists solely so scripts/test-notify-wrapper.sh can prove
# the >=60-minute reminder behavior without a real 60-minute sleep.
_now_epoch() {
  if [ -n "${NOTIFY_TEST_NOW_EPOCH:-}" ]; then
    printf '%s' "$NOTIFY_TEST_NOW_EPOCH"
  else
    date -u +%s
  fi
}

if [ "${1:-}" = "--synthetic-send" ]; then
  check="${2:?usage: run-check-with-alert.sh --synthetic-send <check-name> <ALERT|RECOVERY|REMINDER|TEST> <message>}"
  kind="${3:?missing ALERT|RECOVERY|REMINDER|TEST}"
  message="${4:?missing message}"
  case "$kind" in
    ALERT) priority=4 ;;
    REMINDER) priority=4 ;;
    RECOVERY) priority=3 ;;
    TEST) priority=3 ;;
    *) echo "run-check-with-alert.sh: unknown synthetic kind '$kind' (want ALERT|RECOVERY|REMINDER|TEST)" >&2; exit 2 ;;
  esac
  title="OMNIRA $kind — ${check}"
  if notify_send "$check" "$kind" "$priority" "$title" "$message"; then
    notify_log "$check" "${kind,,}_sent" "synthetic_test"
    echo "synthetic $kind sent for check=$check"
    exit 0
  else
    echo "synthetic $kind send FAILED for check=$check (see NOTIFY_LOG)" >&2
    exit 1
  fi
fi

check_name="${1:?usage: run-check-with-alert.sh <check-name> <command> [args...]}"
shift
if [ $# -eq 0 ]; then
  echo "run-check-with-alert.sh: no check command given" >&2
  exit 2
fi

# --- Run the check exactly once; classify THIS invocation's own output ---
set +e
check_output="$("$@" 2>&1)"
check_exit=$?
set -e
printf '%s\n' "$check_output"

last_line=$(printf '%s\n' "$check_output" | tail -1)
status_word=$(printf '%s\n' "$last_line" | awk '{print $2}')

case "$check_exit" in
  3) severity="CRITICAL" ;;
  1) severity="FAIL" ;;
  0)
    if [ "$status_word" = "WARN" ]; then
      severity="WARN"
    else
      severity="OK"
    fi
    ;;
  *)
    # An exit code neither script is documented to produce. Treat it as
    # FAIL for alerting purposes (fail closed on the unknown), but never
    # invent a severity word the check itself didn't report in its exit
    # code contract.
    severity="FAIL"
    ;;
esac

reason=$(printf '%s\n' "$last_line" | grep -oE 'reason=[^ ]+' || true)
if [ -z "$reason" ]; then
  reason=$(printf '%s\n' "$last_line" | awk '{print $3}')
fi
[ -n "$reason" ] || reason="unspecified"

# --- Per-check lock: read-state -> decide -> send -> write-state is one
# atomic-with-respect-to-other-invocations critical section, so two
# overlapping cron runs of the SAME check can never both send an initial
# ALERT (Section 5). Scope is per check name, never global. ---
mkdir -p "$NOTIFY_STATE_DIR" 2>/dev/null || true
state_file="$NOTIFY_STATE_DIR/${check_name}.state"
lock_file="$NOTIFY_STATE_DIR/${check_name}.lock"
exec 200>"$lock_file"
if ! flock -w "$NOTIFY_LOCK_TIMEOUT_SECONDS" -x 200; then
  echo "run-check-with-alert.sh: could not acquire notification lock for '$check_name' within ${NOTIFY_LOCK_TIMEOUT_SECONDS}s — skipping notification decision this run" >&2
  exit "$check_exit"
fi

prev_severity="OK" # first run / lost state (Section 15): assume a healthy
                    # baseline, so a currently-failing check still alerts
                    # exactly once rather than staying silent forever.
prev_notified_at=0
if [ -r "$state_file" ]; then
  # shellcheck source=/dev/null
  source "$state_file"
  prev_severity="${LAST_SEVERITY:-OK}"
  prev_notified_at="${LAST_NOTIFIED_AT:-0}"
fi

is_failing_class() { [ "$1" = "FAIL" ] || [ "$1" = "CRITICAL" ]; }

now_epoch=$(_now_epoch)
new_notified_at="$prev_notified_at"
notify_layer_failed=0

send_and_record() {
  local kind="$1" priority="$2" action_on_success="$3"
  local title="OMNIRA $kind — ${check_name}"
  if notify_send "$check_name" "$kind" "$priority" "$title" "$last_line"; then
    new_notified_at="$now_epoch"
    notify_log "$check_name" "$action_on_success" "$reason"
  else
    notify_layer_failed=1
    # notify_send already logged the notification_failed line itself.
  fi
}

if is_failing_class "$severity"; then
  if ! is_failing_class "$prev_severity"; then
    send_and_record ALERT 4 alert_sent
  elif [ "$severity" != "$prev_severity" ]; then
    send_and_record ALERT 4 alert_sent
  else
    elapsed=$(( now_epoch - prev_notified_at ))
    if [ "$elapsed" -ge "$NOTIFY_REMINDER_SECONDS" ]; then
      send_and_record REMINDER 4 reminder_sent
    else
      notify_log "$check_name" "suppressed" "$reason"
    fi
  fi
else
  if is_failing_class "$prev_severity"; then
    send_and_record RECOVERY 3 recovery_sent
  else
    : # healthy/degraded -> healthy/degraded: silent, no log noise needed.
  fi
fi

{
  echo "LAST_SEVERITY=$severity"
  echo "LAST_REASON=$reason"
  echo "LAST_NOTIFIED_AT=$new_notified_at"
} > "$state_file.tmp"
sync -f -- "$state_file.tmp" 2>/dev/null || true
mv -f "$state_file.tmp" "$state_file"

flock -u 200

if [ "$check_exit" -ne 0 ]; then
  exit "$check_exit"
fi
if [ "$notify_layer_failed" -eq 1 ]; then
  exit 10
fi
exit 0
