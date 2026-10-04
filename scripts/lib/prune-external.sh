# Retention for the database dumps copied to the external disk. The local copies are pruned at 7 days and
# the cloud copy at 30; the external disk used to keep every dump forever, which also kept group messages
# an administrator had already deleted. Only this database's dump files (and leftover .partial copies) are
# touched, only inside the given directory, and only when it lives under the mount point.
#
# prune_external_dumps <dir> <mount> <db_name> <days>
prune_external_dumps() {
  local dir="$1" mount="$2" db="$3" days="$4"
  case "$days" in ''|*[!0-9]*) echo "== external prune SKIPPED: retention '$days' is not a number" >&2; return 1 ;; esac
  [ "$days" -ge 7 ] || { echo "== external prune SKIPPED: refusing a retention under 7 days" >&2; return 1; }
  [ -n "$dir" ] && [ -n "$mount" ] && [ -n "$db" ] && [ -d "$dir" ] || return 0
  case "$dir" in "$mount"/*) ;; *) echo "== external prune SKIPPED: $dir is not under $mount" >&2; return 1 ;; esac
  find "$dir" -maxdepth 1 -type f \( -name "${db}_*.dump" -o -name "${db}_*.meta.json" -o -name "${db}_*.partial" \) \
    -mtime "+${days}" -print -delete | sed 's/^/== pruned external: /'
}
