#!/usr/bin/env bash
# One-time setup of the encrypted Google Drive backup target.
#
# Writes a DEDICATED rclone config (BACKUP_CLOUD_CONF, default
# ~/.config/omnira/rclone-backup.conf, mode 0600) with two remotes:
#   omnira-gdrive  Google Drive, scope drive.file (the app only sees files it
#                  created), trash disabled so retention really frees space
#   omnira-backup  rclone crypt on top of it (file names + contents encrypted
#                  client-side, passwords generated here, never printed)
#
# It does NOT touch the operator's default rclone config.
#
# The Drive OAuth token needs a browser, so it is supplied from a file. On any
# machine with a browser and rclone installed:
#   rclone authorize "drive" --drive-scope drive.file
# and save the JSON it prints ({"access_token": ...}) to a file, then:
#   scripts/setup-backup-cloud.sh /path/to/token.json
#
# Usage:
#   scripts/setup-backup-cloud.sh <token.json|-> [--force] [--no-verify]
#     --force      replace an existing config (WARNING: a new crypt password
#                  makes every already-uploaded backup unreadable)
#     --no-verify  skip the live upload/check/delete round trip (tests)
set -euo pipefail

CONF=${BACKUP_CLOUD_CONF:-$HOME/.config/omnira/rclone-backup.conf}
TOKEN_SRC=${1:-}
FORCE=0
VERIFY=1
shift || true
for a in "$@"; do
  case "$a" in
    --force) FORCE=1 ;;
    --no-verify) VERIFY=0 ;;
    *) echo "unknown option: $a" >&2; exit 2 ;;
  esac
done

[ -n "$TOKEN_SRC" ] || { sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
command -v rclone >/dev/null || { echo "rclone is not installed" >&2; exit 2; }
command -v openssl >/dev/null || { echo "openssl is not installed" >&2; exit 2; }

if [ -e "$CONF" ] && [ "$FORCE" != 1 ]; then
  echo "refusing to overwrite $CONF (a new crypt password would orphan existing backups); use --force if you really mean it" >&2
  exit 1
fi

if [ "$TOKEN_SRC" = "-" ]; then token=$(cat); else token=$(cat "$TOKEN_SRC"); fi
case "$token" in
  *access_token*) ;;
  *) echo "token file does not look like rclone's OAuth JSON (no access_token)" >&2; exit 1 ;;
esac
token=$(printf '%s' "$token" | tr -d '\n')

umask 077
mkdir -p "$(dirname "$CONF")"
pw1=$(openssl rand -base64 36)
pw2=$(openssl rand -base64 36)
obs1=$(printf '%s' "$pw1" | rclone obscure -)
obs2=$(printf '%s' "$pw2" | rclone obscure -)
unset pw1 pw2

tmp=$(mktemp "$CONF.XXXXXX")
cat > "$tmp" <<EOF
[omnira-gdrive]
type = drive
scope = drive.file
use_trash = false
token = $token

[omnira-backup]
type = crypt
remote = omnira-gdrive:omnira-backup
filename_encryption = standard
directory_name_encryption = true
password = $obs1
password2 = $obs2
EOF
chmod 600 "$tmp"
mv -f "$tmp" "$CONF"
echo "config written: $CONF (0600)"

if [ "$VERIFY" = 1 ]; then
  echo "== live round trip (upload, cryptcheck, delete one small test file)"
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT
  printf 'omnira-backup-setup-%s\n' "$(date -u +%s)" > "$work/setup-check.txt"
  export RCLONE_CONFIG="$CONF"
  rclone copyto "$work/setup-check.txt" "omnira-backup:setup-check/setup-check.txt"
  rclone cryptcheck "$work" "omnira-backup:setup-check" --include setup-check.txt
  rclone delete "omnira-backup:setup-check"
  echo "round trip OK"
fi

cat <<'MSG'

IMPORTANT: the crypt passwords live ONLY in the config file above. If this
host is lost and the file was never copied somewhere safe, every cloud backup
is permanently unreadable. Copy the file to a password manager / sealed
location now (do not paste it into chat or the repo).
MSG
