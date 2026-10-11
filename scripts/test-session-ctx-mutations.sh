#!/usr/bin/env bash
# Mutation test for THE SIGNED SESSION CONTEXT (ADR-0042, migration 000118): weakens each rule in turn (SQL of the migration and the Go signer); the real-Postgres attack tests must go red every time and green on the original. Throwaway database only. Never edit this file while it runs. SKIP=n resumes; REPORT=1 lists survivors instead of stopping.
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-ctxmut-$$; DB=omnira_test_ctxmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/platform/sessionctx/sessionctx.go internal/platform/db/session.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=ctxmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)

KEY=a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1
export OMNIRA_SESSION_CTX_KEY=$KEY
mkdb() { # mkdb <migrations dir>
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
  docker run --rm --network "container:$NAME" -v "$1:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB -e OMNIRA_SESSION_CTX_KEY=$KEY -e SESSION_CTX_MODE=enforce postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
}
run() { # the attack tests (as the application role, mode enforce), the signer's unit tests, and the hub tests that forge the settings
  OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false \
    OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    go test -count=1 -p 1 -run 'TestArbitrarySQL|TestAContextIsAccepted|TestTheApplicationRole|TestTheModes|TestKeyRotation|TestSessionsOpened|TestADelegatedContext|TestSecurityDefinerFunctionsPinPgTempLast|TestRLSHelpersSurviveConnectionReuse|TestMembershipHelpers' ./internal/platform/db 2>&1
  OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false \
    OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    go test -count=1 -p 1 -run 'TestAForgedActing|TestAMalformedActing|TestMembershipPredicatesAreFalseWhileActing|TestTheDelegatedContextIsPinnedToOneInstance|TestTheDelegatedAccessOf|TestDomainAccess' ./internal/hub/adapters 2>&1
  GOFLAGS=-buildvcs=false go test -count=1 ./internal/platform/sessionctx 2>&1
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
sqlmut() { # sqlmut <name> <migration file> <from> <to> [all]   (all: replace every occurrence, for a rule written in two layers)
  N=$((N+1)); if [ "$N" -le "${SKIP:-0}" ]; then echo "   skipped: $1"; return 0; fi
  rm -rf "$WORK/mig"; mkdir -p "$WORK/mig"; cp migrations/*.sql "$WORK/mig/"
  python3 - "$WORK/mig/$2" "$3" "$4" "${5:-}" <<'PY'
import sys
p,f,t,every=sys.argv[1:5]; s=open(p).read()
assert f in s, "SQL mutation target not found: "+f
open(p,'w').write(s.replace(f,t) if every == "all" else s.replace(f,t,1))
PY
  mkdb "$WORK/mig"
  killed "$1" "$(run || true)"
}
M=000118_session_context_signed.up.sql; SC=internal/platform/sessionctx/sessionctx.go
# NOT mutants (equivalent by construction, so nothing can kill them):
#  - clearing the nonce when ctx_begin consumes it: the once-per-transaction rule (base_set) already refuses a second token in that transaction, and the next transaction is issued a new nonce;
#  - the shape checks of ctx_begin on the flag, the nonce and the user, and the tag and field-count checks of ctx_signed: they only run on content that already passed the HMAC, which only the
#    application (or whoever holds the key) can produce; TestAHolderOfTheKeyCannotSendAnythingButTheProtocol proves the protocol is exact even for a holder of the key, and the mutants below
#    that weaken the HMAC itself prove the signature is tested;
#  - hashing both sides before comparing signatures (a constant-time property: a timing difference is not observable by a test);
#  - the length check of ctx_install_key (the table's CHECK (octet_length(key) >= 32) refuses a short key as well: two layers for one rule);
#  - the `NOT (SELECT ctx_acting_present())` term of the six read policies (has_active_membership already answers false while acting: redundancy and a planner hint);
#  - removing ENABLE ROW LEVEL SECURITY from the three registry tables (the privilege revoke already refuses the application role; RLS is only the second wall).

# --- the signature -----------------------------------------------------------------------------------------------------------------------------
sqlmut "any signature is accepted" $M "WHERE sha256(decode(sig, 'hex')) = sha256(public.hmac(convert_to(payload, 'UTF8'), k.key, 'sha256'))" "WHERE true"
sqlmut "a token over a nonce the database did not issue to this transaction is accepted" $M "AND NOT v.base_set AND v.nonce = p[4];" "AND NOT v.base_set;"
sqlmut "a token is accepted against the registry row of an earlier transaction" $M "WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned() AND NOT v.base_set AND v.nonce = p[4];" "WHERE v.pid = pg_backend_pid() AND NOT v.base_set AND v.nonce = p[4];"
sqlmut "a challenge forgets a context already accepted in the transaction" $M "    base_set      = CASE WHEN v.xid = pg_current_xact_id() THEN v.base_set ELSE false END,
    acting_hub    = CASE WHEN v.xid = pg_current_xact_id() THEN v.acting_hub END,
    acting_tenant = CASE WHEN v.xid = pg_current_xact_id() THEN v.acting_tenant END,
    xid           = pg_current_xact_id(),
    nonce         = n;" "    base_set      = false,
    acting_hub    = CASE WHEN v.xid = pg_current_xact_id() THEN v.acting_hub END,
    acting_tenant = CASE WHEN v.xid = pg_current_xact_id() THEN v.acting_tenant END,
    xid           = pg_current_xact_id(),
    nonce         = n;"
sqlmut "the challenge nonce is predictable (constant)" $M "n TEXT := replace(gen_random_uuid()::TEXT, '-', '');" "n TEXT := repeat('a', 32);"
sqlmut "a second context in the same transaction is accepted" $M "AND NOT v.base_set AND v.nonce = p[4];" "AND v.nonce = p[4];"
sqlmut "a refusal does not raise in enforce" $M "IF COALESCE((SELECT mode FROM public.session_ctx_mode), 'enforce') = 'enforce' THEN" "IF false THEN"
sqlmut "a refusal raises in every mode" $M "IF COALESCE((SELECT mode FROM public.session_ctx_mode), 'enforce') = 'enforce' THEN" "IF true THEN"
# --- the registry ---------------------------------------------------------------------------------------------------------------------------------
sqlmut "the verified context outlives its transaction (current_user_id)" $M "SELECT v.uid FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned();
    \$b\$ LANGUAGE sql STABLE%s" "SELECT v.uid FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid();
    \$b\$ LANGUAGE sql STABLE%s"
sqlmut "the verified system flag outlives its transaction" $M "(SELECT v.sys FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned())" "(SELECT v.sys FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid())"
sqlmut "enforce still believes the legacy administrator setting" $M "SELECT COALESCE((SELECT v.sys FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned()), false);" "SELECT COALESCE((SELECT v.sys FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned()), COALESCE(NULLIF(current_setting('app.is_system_admin', TRUE), '')::BOOLEAN, FALSE));"
sqlmut "enforce still believes the legacy user setting" $M "SELECT v.uid FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned();
    \$b\$ LANGUAGE sql STABLE%s\$q\$, sdef);
    EXECUTE format(\$q\$CREATE OR REPLACE FUNCTION public.is_system_admin() RETURNS BOOLEAN AS \$b\$
      SELECT COALESCE((SELECT v.sys" "SELECT COALESCE((SELECT v.uid FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned()), NULLIF(current_setting('app.current_user_id', TRUE), '')::UUID);
    \$b\$ LANGUAGE sql STABLE%s\$q\$, sdef);
    EXECUTE format(\$q\$CREATE OR REPLACE FUNCTION public.is_system_admin() RETURNS BOOLEAN AS \$b\$
      SELECT COALESCE((SELECT v.sys"
sqlmut "enforce still believes the legacy acting setting" $M "SELECT v.acting_hub FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned();" "SELECT COALESCE((SELECT v.acting_hub FROM public.session_ctx_verified v WHERE v.pid = pg_backend_pid() AND v.xid = pg_current_xact_id_if_assigned()), NULLIF(current_setting('app.acting_hub', true), '')::UUID);"
sqlmut "the application role can read the key tables" $M "REVOKE ALL ON session_ctx_keys, session_ctx_mode FROM PUBLIC, omnira_app;" "GRANT SELECT ON session_ctx_keys, session_ctx_mode TO omnira_app;"
sqlmut "the application role can write the verified registry" $M "REVOKE ALL ON session_ctx_verified FROM PUBLIC, omnira_app;" "GRANT INSERT, UPDATE, DELETE, SELECT ON session_ctx_verified TO omnira_app;"
sqlmut "the application role can install a key" $M "REVOKE ALL ON FUNCTION ctx_install_key(BYTEA), ctx_drop_previous_key() FROM PUBLIC, omnira_app;" "REVOKE ALL ON FUNCTION ctx_drop_previous_key() FROM PUBLIC, omnira_app;"
sqlmut "the application role can set the acting context" $M "REVOKE ALL ON FUNCTION ctx_signed(TEXT, TEXT, INTEGER), ctx_set_acting(UUID, UUID), ctx_refused() FROM PUBLIC, omnira_app;" "REVOKE ALL ON FUNCTION ctx_signed(TEXT, TEXT, INTEGER), ctx_refused() FROM PUBLIC, omnira_app;"
sqlmut "the application role can verify signatures (an oracle)" $M "REVOKE ALL ON FUNCTION ctx_signed(TEXT, TEXT, INTEGER), ctx_set_acting(UUID, UUID), ctx_refused() FROM PUBLIC, omnira_app;" "REVOKE ALL ON FUNCTION ctx_set_acting(UUID, UUID), ctx_refused() FROM PUBLIC, omnira_app;"
sqlmut "the application role can change the mode" $M "REVOKE ALL ON FUNCTION ctx_set_mode(TEXT) FROM PUBLIC, omnira_app;" ""
sqlmut "the application role can call the readers' internals" $M "REVOKE ALL ON FUNCTION ctx_measure(TEXT, TEXT, TEXT), ctx_verified(TEXT) FROM PUBLIC, omnira_app;" ""
sqlmut "the application role can run the reader installer" $M "REVOKE ALL ON FUNCTION ctx_install_readers(TEXT) FROM PUBLIC, omnira_app;" ""
# --- keys and modes ---------------------------------------------------------------------------------------------------------------------------------
sqlmut "a rotation drops the previous key at once" $M "UPDATE public.session_ctx_keys SET state = 'previous' WHERE state = 'current';" "DELETE FROM public.session_ctx_keys WHERE state = 'current';"
sqlmut "a second rotation keeps the oldest key" $M "DELETE FROM public.session_ctx_keys WHERE state = 'previous';
  UPDATE" "UPDATE public.session_ctx_keys SET state = 'previous' WHERE state = 'previous' AND false;
  UPDATE"
sqlmut "dropping the previous key does nothing" $M "DELETE FROM public.session_ctx_keys WHERE state = 'previous';
  GET DIAGNOSTICS" "DELETE FROM public.session_ctx_keys WHERE state = 'previous' AND false;
  GET DIAGNOSTICS"
sqlmut "enforce is accepted without a key" $M "IF p_mode <> 'off' AND NOT EXISTS (SELECT 1 FROM public.session_ctx_keys WHERE state = 'current') THEN" "IF false THEN"
sqlmut "measure never reports a divergence" $M "IF p_legacy IS DISTINCT FROM p_verified THEN" "IF false THEN"
# --- the acting context ---------------------------------------------------------------------------------------------------------------------------
sqlmut "lock_served_tenant does not record the acting context" $M "  PERFORM public.ctx_set_acting(p_hub_id, p_tenant_id);
" ""
sqlmut "membership still answers while acting" $M "AND NOT (c.acting_hub IS NOT NULL)
         AND EXISTS (SELECT 1 FROM public.memberships WHERE" "AND EXISTS (SELECT 1 FROM public.memberships WHERE"
sqlmut "a challenge keeps the acting context of an earlier transaction" $M "acting_hub    = CASE WHEN v.xid = pg_current_xact_id() THEN v.acting_hub END," "acting_hub    = v.acting_hub,"
# --- the Go signer ----------------------------------------------------------------------------------------------------------------------------------
mut "the signer always says not-system" $SC 'flag := "0"
	if system {
		flag = "1"
	}' 'flag := "0"'
mut "the signer leaves the user out" $SC 'b1|%s|%s|%s", userID.String(), flag, nonce)' 'b1|%s|%s|%s", "", flag, nonce)'
mut "the signer leaves the nonce out" $SC 'b1|%s|%s|%s", userID.String(), flag, nonce)' 'b1|%s|%s|%s", userID.String(), flag, "")'
mut "the application does not ask for a challenge" $SC 'SELECT public.ctx_challenge()' "SELECT 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'"
mut "a short key is accepted by the application" $SC 'if len(b) < 32 {' 'if len(b) < 1 {'
mut "a malformed key is silently ignored" $SC 'return nil, fmt.Errorf("%s must be hex: %w", KeyEnv, err)' 'return nil, nil'
mut "a refused context is not counted" $SC '	if !ok {
		noteRejected()
	}' '	_ = ok'
mut "the application does not ask the database to verify" $SC "SELECT public.ctx_begin(\$1), pg_catalog.set_config" "SELECT true, pg_catalog.set_config"
mut "the legacy settings are not set next to the signed context (measure would diverge)" $SC "pg_catalog.set_config('app.current_user_id', \$2, true), CASE" "pg_catalog.set_config('app.x', \$2, true), CASE"
mut "the legacy user setting is not set when there is no key" $SC "SELECT set_config('app.current_user_id', \$1, true)" "SELECT set_config('app.x', \$1, true)"
mut "the legacy administrator setting is not set when there is no key" $SC "SELECT set_config('app.is_system_admin', 'true', true)" "SELECT set_config('app.x', 'true', true)"
echo "== all $N mutants killed"
