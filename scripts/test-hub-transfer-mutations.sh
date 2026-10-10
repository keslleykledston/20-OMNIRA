#!/usr/bin/env bash
# Mutation test for DELEGATED TRANSFER, RELEASE AND NOTES (ADR-0040 phase 04c, migration 113): weakens each rule in turn (the three wrappers of the Hub's
# write path and every policy of contact_notes); the real-Postgres tests must go red every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubtrnmut-$$; DB=omnira_test_trnmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/hub/adapters/delegated_writes.go internal/hub/replying/service.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=trnmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)

mkdb() { # mkdb <migrations dir>
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
  docker run --rm --network "container:$NAME" -v "$1:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
}
run() { # the host toolchain with its warm caches
  OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false \
    OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    go test -count=1 -p 1 -run 'TestDelegatedTransferAndRelease|TestDelegatedNotesFollow' \
      ./internal/hub/adapters 2>&1
}
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge 1 ]; }

mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "$out" | tail -25; echo "FAIL: baseline is red"; exit 1; }
echo "== baseline green"

killed() {
  if verdict_green "$2"; then echo "SURVIVED: $1"; [ -n "${REPORT:-}" ] && return 0; echo "FAIL: mutation survived"; exit 1; fi
  [ "$(echo "$2" | grep -c -- '--- FAIL')" -ge 1 ] || { echo "$2" | tail -12; echo "FAIL: mutation '$1' was inconclusive (no failing test; build error?)"; exit 1; }
  echo "   killed: $1 ($(echo "$2" | grep -c -- '--- FAIL') failing tests)"
}
N=0
mut() { # mut <name> <file> <from> <to>
  N=$((N+1)); if [ "$N" -le "${SKIP:-0}" ]; then echo "   skipped: $1"; return 0; fi
  python3 - "$WORK/orig" "$2" "$3" "$4" <<'PY'
import sys
orig,f,a,b=sys.argv[1:5]
s=open(orig+"/"+f).read()
assert a in s, "mutation target not found: "+a
open(f,'w').write(s.replace(a,b,1))
PY
  killed "$1" "$(run || true)"
  cp "$WORK/orig/$2" "$2"
}
sqlmut() { # sqlmut <name> <migration file> <from> <to>
  N=$((N+1)); if [ "$N" -le "${SKIP:-0}" ]; then echo "   skipped: $1"; return 0; fi
  rm -rf "$WORK/mig"; mkdir -p "$WORK/mig"; cp migrations/*.sql "$WORK/mig/"
  python3 - "$WORK/mig/$2" "$3" "$4" <<'PY'
import sys
p,f,t=sys.argv[1:4]; s=open(p).read()
assert f in s, "SQL mutation target not found: "+f
open(p,'w').write(s.replace(f,t,1))
PY
  mkdb "$WORK/mig"
  killed "$1" "$(run || true)"
}
# NOT mutants (equivalent by construction; the same outcome comes from a second layer, so no test could tell them apart):
#   - permission checker "any user id accepted": actor_has_permission answers false for anybody but the session's own person (second layer, tested through the
#     checker in the ticket test);
#   - delegated resolver "no credential reference" / "credential cannot be decrypted": the next check answers the SAME resolution code (credential not found /
#     invalid) and the same HTTP 503;
#   - materialize "source not validated": an unknown source would reach the CHECK constraint on account_external_links.source and fail there;
#   - tickets update "queue scope ignored": an UPDATE needs the row to be visible, and the SELECT policy (tested) already carries the scope;
#   - the explicit scope re-check in the UPDATE WITH CHECK of tickets / attempts / evidence (Codex 04b HIGH): PostgreSQL also checks the NEW row of an UPDATE
#     against the SELECT policy, which already carries the scope, so removing it changes nothing observable (the re-point tests prove the behaviour);
#   - the acting_one_instance pins on tickets / attempts / evidence: second layer on writable tables (the permissive policies hang from a pinned conversation
#     or contact), like contacts / contact_account_links in migration 111.
# NOT mutants (equivalent by construction; a second layer gives the same outcome, so no test could tell them apart):
#   - contact_notes policies without `acting_hub() IS NOT NULL`: outside the delegated context actor_has_permission asks the member's ROLES for contact.note, which
#     no role holds, and members reach the table through their own policies;
#   - the key check in the UPDATE / DELETE policies of contact_notes: both statements need the row to be visible, and the SELECT policy already asks for the key;
#   - the author check in ONE of USING / WITH CHECK of the UPDATE policy: the other still refuses (only removing both changes anything, which is a mutant below);
#   - the acting_one_instance pin on contact_notes: second layer, the permissive policies hang from `contacts`, which carries its own pin (migration 111);
#   - the editor's key in the notes handler: the route is behind Delegable(contact.note), which refuses first (pinned in delegable_routes_test.go).
DW=internal/hub/adapters/delegated_writes.go; RP=internal/hub/replying/service.go

mut "transfer: an empty target is accepted (becomes a 409, not a 400)" $DW 'err != nil || req.TargetUserID == uuid.Nil {' 'err != nil {'
mut "transfer: hands to nobody (gives back) instead of the person chosen" $DW 'd.reply.Transfer(r.Context(), tc.ActorID, t, &req.TargetUserID)' 'd.reply.Transfer(r.Context(), tc.ActorID, t, nil)'
mut "release: does nothing" $DW 'd.reply.Transfer(r.Context(), tc.ActorID, t, nil); err != nil {' 'error(nil); err != nil {'
mut "candidates: a member gets an (empty) answer instead of a 404" $DW '		if !delegated {
			httpError(w, "not found", http.StatusNotFound)
			return
		}
		t, ok := d.target(w, r, tc)
		if !ok {
			return
		}
		items, err' '		if !delegated {
			writeJSON(w, map[string]any{"items": []any{}})
			return
		}
		t, ok := d.target(w, r, tc)
		if !ok {
			return
		}
		items, err'
mut "directory: offers the holder to themself" $RP 'WHERE hm.hub_id = $1 AND u.id <> $2 AND has_active_hub_access(' 'WHERE hm.hub_id = $1 AND $2::uuid IS NOT NULL AND has_active_hub_access('
mut "directory: offers people who cannot reply" $RP 'has_active_hub_access(u.id, $3, $4, $1, true, true)
			ORDER BY 4' 'has_active_hub_access(u.id, $3, $4, $1, true, false)
			ORDER BY 4'
mut "transfer: the chosen person is not proven again" $RP '			ok, err := distribution.CanTake(c, q, t.Item.HubID, t.Item.TenantID, queue, *to)' '			ok, err := distribution.CanTake(c, q, t.Item.HubID, t.Item.TenantID, queue, *to); ok = true'

M=000113_hub_delegated_transfer_and_notes.up.sql
sqlmut "keys: contact.note has no data domain (not delegable)" $M "  ('conversation.transfer', 'conversation', 'write'),
  ('contact.note',          'contact',      'read');" "  ('conversation.transfer', 'conversation', 'write');"
sqlmut "keys: conversation.transfer has no data domain (not delegable)" $M "  ('conversation.transfer', 'conversation', 'write'),
  ('contact.note',          'contact',      'read');" "  ('contact.note',          'contact',      'read');"
sqlmut "keys: contact.note maps to the contact WRITE domain (hands note holders the contact UPDATE policies)" $M "  ('contact.note',          'contact',      'read');" "  ('contact.note',          'contact',      'write');"
sqlmut "notes read: any contact reader reads them (the key is not asked)" $M "CREATE POLICY contact_notes_read_delegated ON contact_notes FOR SELECT
  USING (acting_hub() IS NOT NULL
         AND actor_has_permission(tenant_id, current_user_id(), 'contact.note')" "CREATE POLICY contact_notes_read_delegated ON contact_notes FOR SELECT
  USING (acting_hub() IS NOT NULL
         AND true"
sqlmut "notes read: the scope of the contact is ignored" $M "         AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_notes.contact_id AND ct.tenant_id = contact_notes.tenant_id));
CREATE POLICY contact_notes_insert_delegated" ");
CREATE POLICY contact_notes_insert_delegated"
sqlmut "notes insert: the key is not asked" $M "  WITH CHECK (acting_hub() IS NOT NULL
              AND actor_has_permission(tenant_id, current_user_id(), 'contact.note')
              AND created_by_user_id = current_user_id()
              AND EXISTS" "  WITH CHECK (acting_hub() IS NOT NULL
              AND created_by_user_id = current_user_id()
              AND EXISTS"
sqlmut "notes insert: in another person's name" $M "              AND created_by_user_id = current_user_id()
              AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_notes.contact_id AND ct.tenant_id = contact_notes.tenant_id));
CREATE POLICY contact_notes_update_delegated" "              AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_notes.contact_id AND ct.tenant_id = contact_notes.tenant_id));
CREATE POLICY contact_notes_update_delegated"
sqlmut "notes insert: the scope of the contact is ignored" $M "              AND created_by_user_id = current_user_id()
              AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_notes.contact_id AND ct.tenant_id = contact_notes.tenant_id));
CREATE POLICY contact_notes_update_delegated" "              AND created_by_user_id = current_user_id());
CREATE POLICY contact_notes_update_delegated"
sqlmut "notes update: any author (USING and WITH CHECK)" $M "         AND created_by_user_id = current_user_id()
         AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_notes.contact_id AND ct.tenant_id = contact_notes.tenant_id))
  WITH CHECK (acting_hub() IS NOT NULL
              AND actor_has_permission(tenant_id, current_user_id(), 'contact.note')
              AND created_by_user_id = current_user_id());" "         AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_notes.contact_id AND ct.tenant_id = contact_notes.tenant_id))
  WITH CHECK (acting_hub() IS NOT NULL
              AND actor_has_permission(tenant_id, current_user_id(), 'contact.note'));"
sqlmut "notes delete: any author" $M "CREATE POLICY contact_notes_delete_delegated ON contact_notes FOR DELETE
  USING (acting_hub() IS NOT NULL
         AND actor_has_permission(tenant_id, current_user_id(), 'contact.note')
         AND created_by_user_id = current_user_id()" "CREATE POLICY contact_notes_delete_delegated ON contact_notes FOR DELETE
  USING (acting_hub() IS NOT NULL
         AND actor_has_permission(tenant_id, current_user_id(), 'contact.note')"
echo "PASS: all $N mutants killed"
