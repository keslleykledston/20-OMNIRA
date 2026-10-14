#!/usr/bin/env bash
# Mutation test for the Headless / external-application invariants (ADR-0046): weakens the Go code and the SQL of the credential, the
# authentication, the in-transaction recheck and the row policies in many ways; the real-Postgres suite must go red every time and green
# on the original. Throwaway database only. Sections are added phase by phase (H1 credentials/authentication; H2 control; H4 journal; H5 writes).
#   usage: scripts/test-headless-mutations.sh [phase ...]   (default: every phase that exists)
set -euo pipefail
cd "$(dirname "$0")/.."
PHASES=${*:-"h1 h2 h3"}
NAME=omnira-headlessmut-$$; DB=omnira_test_headlessmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/external/adapters/write_http.go internal/messages/application/external_send.go internal/messages/adapters/outbound_external.go internal/conversationcontrol/service.go internal/attendance/application/service.go internal/external/delivery/worker.go internal/external/delivery/sign.go internal/external/netpolicy/ssrf.go internal/external/adapters/events_http.go internal/external/adapters/webhook_admin_http.go internal/external/adapters/event_visibility.go internal/media/adapters/reader.go internal/flows/adapters/effects_postgres.go internal/external/adapters/visibility.go internal/external/adapters/read_http.go internal/external/adapters/read_dto.go internal/messages/adapters/attachment_postgres.go internal/external/domain/token.go internal/external/application/auth.go internal/external/adapters/middleware.go internal/external/adapters/recheck.go internal/external/adapters/admin_http.go internal/external/adapters/store.go internal/external/adapters/problem.go internal/conversationcontrol/service.go internal/conversationcontrol/http.go internal/routing/engine/engine.go internal/routing/adapters/assign.go internal/routing/adapters/postgres.go internal/messages/adapters/postgres.go internal/messages/adapters/attachment_postgres.go internal/messages/adapters/outbound_system.go internal/attendance/adapters/postgres.go internal/flows/application/engine.go internal/hub/replying/service.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=headlessmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)

mkdb() { # mkdb <migrations dir>
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
  docker run --rm --network "container:$NAME" -v "$1:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
}
T_PKGS="./internal/external/adapters ./internal/external/application"; T_RUN=""; T_NEED=2; MIGDIR=""
run() { # the host toolchain with its warm module and build caches
  OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false OMNIRA_TEST_MIGRATIONS_DIR="$MIGDIR" \
    OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    go test -count=1 -timeout 180s ${T_RUN:+-run "$T_RUN"} $T_PKGS 2>&1
}
# NOTE: exit status of a pipeline ending in grep is grep's, so the verdict is read from the output.
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge "$T_NEED" ]; }

mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "$out" | tail -20; echo "FAIL: baseline is red"; exit 1; }
echo "== baseline green"

killed() { # killed <name> <output>
  if verdict_green "$2"; then echo "FAIL: mutation '$1' SURVIVED"; exit 1; fi
  echo "$2" | grep -q "^FAIL" || { echo "$2" | tail -5; echo "FAIL: mutation '$1' was inconclusive"; exit 1; }
  if [ "$(echo "$2" | grep -c -- '--- FAIL')" -eq 0 ]; then echo "$2" | tail -8; echo "FAIL: mutation '$1' only broke the build (not a real kill)"; exit 1; fi
  echo "   killed: $1 ($(echo "$2" | grep -c -- '--- FAIL') failing tests)"
}
N=0; FROM=${MUT_FROM:-1}   # MUT_FROM=<n> resumes at the n-th mutant (a long run that stopped on a survivor need not repeat the ones already killed)
skip() { N=$((N+1)); [ "$N" -lt "$FROM" ]; }
mut() { # mut <name> <file> <from> <to> [<from> <to> ...]   (the first occurrence of each pair; several pairs fall together, for defences repeated on purpose)
  skip && return 0
  local name="$1" file="$2"; shift 2
  python3 - "$file" "$WORK/orig/$file" "$@" <<'PY'
import sys
p,orig=sys.argv[1:3]
rest=sys.argv[3:]
s=open(orig).read()
for i in range(0,len(rest),2):
    f,t=rest[i],rest[i+1]
    assert f in s, "mutation target not found: "+f
    s=s.replace(f,t,1)
open(p,'w').write(s)
PY
  killed "$name" "$(run || true)"
  cp "$WORK/orig/$file" "$file"
}
mutall() { # mutall <name> <file> <from> <to>   (EVERY occurrence: a defence repeated on purpose has to fall as a whole)
  skip && return 0
  python3 - "$2" "$WORK/orig/$2" "$3" "$4" <<'PY'
import sys
p,orig,f,t=sys.argv[1:5]
s=open(orig).read()
assert f in s, "mutation target not found: "+f
open(p,'w').write(s.replace(f,t))
PY
  killed "$1" "$(run || true)"
  cp "$WORK/orig/$2" "$2"
}
sqlmut() { # sqlmut <name> <migration file> <from> <to>
  skip && return 0
  rm -rf "$WORK/mig"; mkdir -p "$WORK/mig"; cp migrations/*.sql "$WORK/mig/"; MIGDIR="$WORK/mig"
  python3 - "$WORK/mig/$2" "$3" "$4" <<'PY'
import sys
p,f,t=sys.argv[1:4]; s=open(p).read()
assert f in s, "SQL mutation target not found: "+f
open(p,'w').write(s.replace(f,t,1))
PY
  mkdb "$WORK/mig"
  killed "$1" "$(run || true)"
  MIGDIR=""
}

for phase in $PHASES; do
case "$phase" in
h1)
  echo "== H1: credentials, authentication, recheck, policies"
  T=internal/external/domain/token.go; A=internal/external/application/auth.go; M=internal/external/adapters/middleware.go
  R=internal/external/adapters/recheck.go; H=internal/external/adapters/admin_http.go; S=internal/external/adapters/store.go
  mut "any secret matches the stored digest"                $T "return subtle.ConstantTimeCompare(d, storedDigest) == 1" "return subtle.ConstantTimeCompare(d, storedDigest) == 1 || true"
  mut "the secret is never compared"                        $A "if !domain.SecretMatches(secret, rec.Digest) {" "if false {"
  mut "a stored scope this build does not know is accepted" $A "	scopes, err := domain.ParseScopes(rec.Scopes)
	if err != nil {" "	scopes, _ := domain.ParseScopes(rec.Scopes)
	if false {"
  mut "the tenant can be chosen by query"                   $M "r.URL.Query().Has(\"tenant_id\") ||" "false ||"
  mut "the tenant can be chosen by header"                  $M "|| r.Header.Get(\"X-Tenant-ID\") != \"\"" "|| false"
  mut "acting-as header is tolerated"                       $M "|| r.Header.Get(\"X-Omnira-Acting-As\") != \"\"" "|| false"
  mut "no per-installation rate limit"                      $M "if allowed, wait := m.perApp.Allow(\"app:\"+p.InstallationID.String(), p.RatePerMin); !allowed {" "if allowed, wait := m.perApp.Allow(\"app:\"+p.InstallationID.String(), p.RatePerMin); !allowed && false {"
  mut "failed authentication is never throttled"            $M "if m.FailedAuthPerMinute > 0 {" "if false {"
  mut "scope is not checked"                                $M "if !p.Has(scope) {" "if !p.Has(scope) && false {"
  mut "the throttle keys on the proxy, not on the client"   $M "(ip.IsLoopback() || ip.IsPrivate()) {" "(false) {"
  mut "a public peer may choose its own throttle key"       $M "ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {" "ip != nil {"
  mut "audit rows ignore the request id"                    $H "correlation = u // the log line and the audit row of one request share the id" "_ = u"
  mut "the recheck always passes"                           $R "if !ok {" "if false {"
  mut "unknown JSON fields are accepted by the admin API"   $H "dec.DisallowUnknownFields()" "_ = dec"
  mut "a revoked installation can still be edited"          $S "	if cur.Status == domain.StatusRevoked {
		return cur, domain.ErrInstallationRevoked
	}
	if in.Name != nil {" "	if in.Name != nil {"
  mut "an installation can be revoked twice"                $S "case t == Revoke && cur.Status != domain.StatusRevoked:" "case t == Revoke:"
  mut "more than two live credentials are allowed"          $S "if live >= domain.MaxLiveCredentials {" "if false {"
  mut "the credential is not revoked with the installation" $S "UPDATE external_app_credentials SET revoked_at = now() WHERE tenant_id = \$1 AND installation_id = \$2 AND revoked_at IS NULL\`, tenantID, id); err == nil {" "SELECT 1\`); err == nil {"
  sqlmut "lookup ignores the installation status"           000135_external_app_installations.up.sql "     AND i.status = 'active'
     AND t.status = 'active'
\$\$;" "     AND t.status = 'active'
\$\$;"
  sqlmut "lookup ignores the company status"                000135_external_app_installations.up.sql "     AND t.status = 'active'
\$\$;" "\$\$;"
  sqlmut "lookup ignores credential expiry"                 000135_external_app_installations.up.sql "     AND c.revoked_at IS NULL AND c.expires_at > now()
     AND i.status = 'active'" "     AND c.revoked_at IS NULL
     AND i.status = 'active'"
  sqlmut "recheck does not lock (revocation does not wait)" 000135_external_app_installations.up.sql "      FOR SHARE OF i, c, t;" "      ;"
  sqlmut "recheck ignores a revoked credential"             000135_external_app_installations.up.sql "      AND c.id = p_credential AND c.revoked_at IS NULL AND c.expires_at > clock_timestamp()" "      AND c.id = p_credential"
  sqlmut "recheck is not bound to the tenant"               000135_external_app_installations.up.sql "    WHERE i.tenant_id = p_tenant AND i.id = p_installation AND i.status = 'active'
      AND c.id = p_credential" "    WHERE i.id = p_installation AND i.status = 'active'
      AND c.id = p_credential"
  sqlmut "touch is not bound to the installation"           000135_external_app_installations.up.sql "   WHERE tenant_id = p_tenant AND installation_id = p_installation AND id = p_credential" "   WHERE id = p_credential"
  sqlmut "allow-list door is not bound to the tenant"       000135_external_app_installations.up.sql "   WHERE i.tenant_id = p_tenant AND i.id = p_installation AND i.status = 'active'
\$\$;" "   WHERE i.id = p_installation AND i.status = 'active'
\$\$;"
  sqlmut "the application role can read the digest"         000135_external_app_installations.up.sql "FROM PUBLIC, omnira_app;" "FROM PUBLIC;"
  sqlmut "a system session can read installations"          000135_external_app_installations.up.sql "FOR SELECT USING (actor_has_permission(tenant_id, current_user_id(), 'integration.read'))\$p\$, t || '_read', t);" "FOR SELECT USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'integration.read'))\$p\$, t || '_read', t);"
  sqlmut "recheck does not lock the company (suspension does not wait)" 000135_external_app_installations.up.sql "      FOR SHARE OF i, c, t;" "      FOR SHARE OF i, c;"
  sqlmut "recheck judges expiry with the start of the transaction"      000135_external_app_installations.up.sql "c.expires_at > clock_timestamp()" "c.expires_at > now()"
  sqlmut "allow-list door ignores the company status"                   000135_external_app_installations.up.sql "    JOIN tenants t ON t.id = i.tenant_id AND t.status = 'active'
   WHERE i.tenant_id = p_tenant AND i.id = p_installation AND i.status = 'active'" "   WHERE i.tenant_id = p_tenant AND i.id = p_installation AND i.status = 'active'"
  sqlmut "a queue of another company is accepted in the allow-list" 000135_external_app_installations.up.sql "  FOREIGN KEY (tenant_id, queue_id) REFERENCES queues (tenant_id, id) ON DELETE CASCADE" "  FOREIGN KEY (queue_id) REFERENCES queues (id) ON DELETE CASCADE"
  ;;
h2)
  echo "== H2: typed control, guard, canonical service, legacy writers"
  T_PKGS="./internal/conversationcontrol ./internal/messages/adapters ./internal/flows/application"; T_NEED=3
  T_RUN="TestTheBotNeverActs|TestEveryOutboundTargetExcludesAnExternalHold|TestEveryFreeCheckAsksAboutTheExternalControl|TestSupervisor|TestNo|TestClaim|TestGuard|TestDrain|TestFinal|TestExactly|TestControl|TestMigration|TestAnAgent"
  CS=internal/conversationcontrol/service.go; CH=internal/conversationcontrol/http.go; EN=internal/routing/engine/engine.go
  RA=internal/routing/adapters/assign.go; RP=internal/routing/adapters/postgres.go; MP=internal/messages/adapters/postgres.go
  MS=internal/messages/adapters/outbound_system.go; AT=internal/attendance/adapters/postgres.go
  M=000136_conversation_control.up.sql
  mut "the engine assigns over an application's hold"       $EN "	case c.held: // an external application holds it" "	case false: // an external application holds it" "		if held { // an application took it while we were choosing: leave it alone" "		if false { // an application took it while we were choosing: leave it alone"
  mut "the manual path does not see the hold"                $RA "	if held {
		return nil, true, ports.ErrHeldByExternal
	}" "	_ = held"
  mutall "the legacy round robin and claim ignore the hold"  $RP " AND control_kind IS NULL" ""
  mut "LoadSendContext does not report the hold"             $MP "COALESCE(c.control_kind IN ('crm_application','crm_agent_verified'), false)
		FROM conversations c" "false
		FROM conversations c"
  mut "a person's reply may land in an application's hold"   $MP "		    AND COALESCE(c.control_kind, '') NOT IN ('crm_application','crm_agent_verified')" "		    AND true"
  mut "the bot may speak over an application's hold"         $MS " AND c.control_kind IS NULL" ""
  mut "an application claims over a person"                  $CS "	if !cur.Free() {
		return cur, false, ErrHeldByOther
	}
	if ifMatch != nil && *ifMatch != cur.Version {" "	if ifMatch != nil && *ifMatch != cur.Version {"
  mut "a repeated claim ignores a stale version"             $CS "		if ifMatch != nil && *ifMatch != cur.Version {
			return cur, false, ErrStaleVersion
		}
		return cur, false, nil" "		return cur, false, nil"
  mut "a claim of a free conversation ignores the version"   $CS "	if !cur.Free() {
		return cur, false, ErrHeldByOther
	}
	if ifMatch != nil && *ifMatch != cur.Version {
		return cur, false, ErrStaleVersion
	}" "	if !cur.Free() {
		return cur, false, ErrHeldByOther
	}"
  mut "a closed conversation can be claimed"                 $CS "	if cur.Status != \"open\" {
		return cur, false, ErrClosed
	}" "	_ = cur.Status"
  mut "release ignores the version"                          $CS "	if ifMatch != cur.Version {
		return cur, ErrStaleVersion
	}" "	_ = ifMatch"
  mut "anyone can release"                                   $CS "	if !cur.HeldBy(KindCRMApplication, installationID) {
		return cur, ErrNotHolder
	}" "	_ = installationID"
  mut "a release does not ask the router to look again"      $CS "AND qq.mode IN ('round_robin', 'least_loaded')" "AND true"
  mut "the drain frees another installation's conversations" $CS "AND control_holder_id = \$2" ""
  mut "the drain frees another company's conversations"      $CS "WHERE tenant_id = \$1 AND control_kind = 'crm_application'" "WHERE control_kind = 'crm_application'"
  mut "a takeover needs no external hold"                    $CS "	if !cur.HeldExternally() {
		return cur, ErrNotExternal
	}" "	_ = cur"
  mut "a takeover of a finalized conversation"               $CS "	if cur.Status != \"open\" {
		return cur, ErrClosed
	}
	if !cur.HeldExternally() {" "	if !cur.HeldExternally() {"
  mut "a takeover needs no reason"                           $CS "	if r == \"\" {" "	if false {"
  mut "closing frees a person's hold too"                    $CS "	if !cur.HeldExternally() {
		return false, nil
	}" "	_ = cur"
  mut "finalizing leaves the application holding"            $AT "	if _, err := (conversationcontrol.Service{}).ReleaseOnClose(ctx, r.q(ctx), tenant, conversationID); err != nil {
		return err
	}" "	_ = conversationcontrol.Service{}"
  mut "the takeover needs no confirmation"                   $CH "!errors.Is(err, io.EOF) || !req.Confirm {" "!errors.Is(err, io.EOF) {"
  mut "the takeover needs no permission"                     $CH "	if !ok {
		http.Error(w, \"forbidden\", http.StatusForbidden)
		return
	}
	conversationID, err" "	_ = ok
	conversationID, err"
  mut "the takeover accepts unknown fields"                  $CH "dec.DisallowUnknownFields()" "_ = dec"
  mut "the bot starts on an application's conversation"      internal/flows/application/engine.go "f.AssignedTo != nil || f.HeldExternally ||" "f.AssignedTo != nil ||"
  mut "the bot controls an application's conversation"       internal/flows/application/engine.go " && f.AssignedTo == nil && !f.HeldExternally" " && f.AssignedTo == nil"
  mut "an attachment may land in an application's hold"      internal/messages/adapters/attachment_postgres.go "		    AND COALESCE(c.control_kind, '') NOT IN ('crm_application','crm_agent_verified')" "		    AND true"
  sqlmut "the guard lets a legacy writer take an application's hold" $M "    IF OLD.control_kind IN ('crm_application', 'crm_agent_verified') THEN
      RAISE EXCEPTION 'conversation is held by an external application' USING ERRCODE = 'OM001';
    END IF;" "    IF false THEN
      RAISE EXCEPTION 'conversation is held by an external application' USING ERRCODE = 'OM001';
    END IF;"
  sqlmut "a Hub agent or another application can overwrite the hold" $M "AND (NEW.control_kind = 'hub_user' OR (NEW.control_kind IN ('crm_application', 'crm_agent_verified') AND NEW.control_holder_id IS DISTINCT FROM OLD.control_holder_id)) THEN" "AND false THEN"
  sqlmut "a change of holder may skip the version"         $M "    IF NEW.control_version <> OLD.control_version + 1 THEN" "    IF false THEN"
  sqlmut "the holder need not exist"                       $M "AND NOT external_installation_exists(NEW.tenant_id, NEW.control_holder_id) THEN" "AND false THEN"
  sqlmut "the holder may live in another company"          $M "WHERE tenant_id = p_tenant AND id = p_installation) \$\$;" "WHERE id = p_installation) \$\$;"
  sqlmut "an application holder may coexist with an assignee" $M "control_origin IS NOT NULL
        AND assigned_to_user_id IS NULL));" "control_origin IS NOT NULL));"
  sqlmut "the realtime function ignores the control"       $M "OR NEW.status IS DISTINCT FROM OLD.status
        OR NEW.control_version IS DISTINCT FROM OLD.control_version THEN" "OR NEW.status IS DISTINCT FROM OLD.status THEN"
  sqlmut "the realtime trigger does not watch the control" $M "AFTER INSERT OR UPDATE OF assigned_to_user_id, status, control_version ON conversations" "AFTER INSERT OR UPDATE OF assigned_to_user_id, status ON conversations"
  sqlmut "the history can be rewritten"                    $M "REVOKE ALL ON conversation_control_events FROM PUBLIC, omnira_app;" "REVOKE ALL ON conversation_control_events FROM PUBLIC;"
  sqlmut "anyone can write the history"                    $M "WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'conversation.takeover_external'));" "WITH CHECK (true);"
  sqlmut "supervisors lose the takeover permission"        $M "r.key IN ('tenant_admin', 'tenant_supervisor') AND p.key = 'conversation.takeover_external'" "r.key IN ('tenant_admin') AND p.key = 'conversation.takeover_external'"
  sqlmut "the backfill calls every holder a member"        $M "THEN 'tenant_user' ELSE 'hub_user' END," "THEN 'tenant_user' ELSE 'tenant_user' END,"
  T_PKGS="./internal/conversationcontrol"; T_NEED=1; T_RUN="TestOnlyTheControlService|TestTheControlHistory|TestFlowEffects|TestGuard|TestClaim"
  sqlmut "the capability check accepts any session (current_user, not session_user)" $M "SELECT r.rolsuper FROM pg_roles r WHERE r.rolname = session_user" "SELECT r.rolsuper FROM pg_roles r WHERE r.rolname = current_user"
  sqlmut "the capability outlives the transaction"        $M "AND c.xid = pg_current_xact_id_if_assigned() AND c.tenant_id = p_tenant" "AND c.tenant_id = p_tenant"
  sqlmut "the capability opens every company"             $M "AND c.xid = pg_current_xact_id_if_assigned() AND c.tenant_id = p_tenant)" "AND c.xid = pg_current_xact_id_if_assigned())"
  sqlmut "an agent may obtain the capability"             $M "IF NOT (is_system_admin() OR actor_has_permission(p_tenant, current_user_id(), 'conversation.takeover_external')) THEN" "IF false THEN"
  sqlmut "the guard does not ask for the capability"      $M "AND (touched OR NEW.assigned_to_user_id IS DISTINCT FROM OLD.assigned_to_user_id) AND NOT conversation_control_cap_valid(OLD.tenant_id) THEN" "AND false THEN"
  sqlmut "a hold can be forged at INSERT"                 $M "IF NEW.control_kind IN ('crm_application', 'crm_agent_verified') AND NOT conversation_control_cap_valid(NEW.tenant_id) THEN" "IF false THEN"
  sqlmut "the history needs no capability"                $M "  IF NOT conversation_control_cap_valid(p_tenant) THEN
    RAISE EXCEPTION 'not allowed to write the control history'" "  IF false THEN
    RAISE EXCEPTION 'not allowed to write the control history'"
  sqlmut "anyone may insert into the history (the grant AND the missing INSERT policy are repeated defences)" $M "GRANT SELECT ON conversation_control_events TO omnira_app;" "GRANT SELECT, INSERT ON conversation_control_events TO omnira_app; CREATE POLICY conversation_control_events_forge ON conversation_control_events FOR INSERT WITH CHECK (true);"
  mut "a flow requeues an application's hold"             internal/flows/adapters/effects_postgres.go "	    AND c.control_kind IS NULL -- an external application's hold is not requeued by a flow (ADR-0046)
" ""
  mut "a flow hands off an application's hold"            internal/flows/adapters/effects_postgres.go "WHERE tenant_id=\$1 AND id=\$2 AND control_kind IS NULL\`" "WHERE tenant_id=\$1 AND id=\$2\`"
  T_PKGS="./internal/hub/adapters"; T_NEED=1; T_RUN="TestTheDelegatedReclassificationSideEffectsCheckTheKeyThemselves"
  sqlmut "the delegated dequeue takes a conversation an application holds" $M "AND c.assigned_to_user_id IS NULL AND c.control_kind IS NULL AND c.queue_id IS NOT NULL
    AND EXISTS (SELECT 1 FROM public.contacts ct" "AND c.assigned_to_user_id IS NULL AND c.queue_id IS NOT NULL
    AND EXISTS (SELECT 1 FROM public.contacts ct"
  T_RUN="TestHubReply_RefusesAConversationAnExternalApplicationHolds"
  T_PKGS="./internal/hub/adapters"; T_NEED=1
  HS=internal/hub/replying/service.go
  mut "the Hub claims what an application holds"           $HS "	if held { // after the access check" "	if false { // after the access check"
  mut "the Hub claim is not typed"                         $HS "control_kind = 'hub_user', control_holder_id = \$3, control_version = control_version + 1, control_since = now(), control_origin = 'hub'
		                        WHERE" "
		                        WHERE"
  ;;
h3)
  echo "== H3: visibility, pagination, control document, messages, media"
  T_PKGS="./internal/external/adapters"; T_NEED=1
  T_RUN="TestReadAPI|TestEveryStatementOfTheExternalReadSide"
  V=internal/external/adapters/visibility.go; R=internal/external/adapters/read_http.go; D=internal/external/adapters/read_dto.go
  mut "staff conversations are visible"                      $V "		alias + \".internal_user_id IS NULL\",
		alias + \".conversation_kind <> 'internal'\"," "		\"true\",
		\"true\"," "		\"EXISTS (SELECT 1 FROM contacts vc WHERE vc.tenant_id = \" + alias + \".tenant_id AND vc.id = \" + alias + \".contact_id AND vc.kind NOT IN ('internal', 'spam'))\"," "		\"true\","
  mut "spam contacts are visible"                            $V "vc.kind NOT IN ('internal', 'spam')" "vc.kind NOT IN ('internal')"
  mut "internal contacts are visible"                        $V "vc.kind NOT IN ('internal', 'spam')" "vc.kind NOT IN ('spam')"
  mut "the queue allow-list is ignored"                      $V "	if len(v.QueueIDs) > 0 {" "	if false {"
  mut "the line allow-list is ignored"                       $V "	if len(v.ChannelIDs) > 0 {" "	if false {"
  mut "another company's conversations are visible"          $V "		alias + \".tenant_id = \" + t," "		\"true\" + t[:0],"
  mut "the control version is not the ETag"                  $R "	w.Header().Set(\"ETag\", fmt.Sprintf(\`\"control-%d\"\`, d.Control.Version))" "	w.Header().Set(\"ETag\", fmt.Sprintf(\`\"x-%d\"\`, d.Control.Version))"
  mut "limit 0 is accepted"                                  $R "n < 1 || n > maxPageSize" "n < 0 || n > maxPageSize"
  mut "limit above the maximum is accepted"                  $R "n < 1 || n > maxPageSize" "n < 1"
  mut "pages repeat their last row"                          $R "where += \" AND (\" + sortCol + \", c.id) < (\"" "where += \" AND (\" + sortCol + \", c.id) <= (\""
  mut "message pages repeat their last row"                  $R "order, cmp := \"ASC\", \">\"" "order, cmp := \"ASC\", \">=\""
  mut "the messages of a hidden conversation are an empty page" $R "		if !exists {
			return errNotVisible
		}" "		_ = exists"
  mut "the media of a hidden conversation is served"         $R "WHERE \`+vis+\` LIMIT 1" "WHERE c.tenant_id = \$1 AND (\`+vis+\` OR true) LIMIT 1"
  mut "every media type is served inline"                    $R "	if isInlineSafeMime(served.Mime) {" "	if true {"
  mut "media is served without nosniff"                      $R "	hd.Set(\"X-Content-Type-Options\", \"nosniff\")" "	_ = hd"
  mut "an installation may claim a held conversation (can)"  $D "Claim:    open && c.State == \"unassigned\" && p.Has(domain.ScopeConversationClaim)," "Claim:    open && p.Has(domain.ScopeConversationClaim),"
  mut "a media id that is not the message's is served"       $R "		if err == nil && served.ID != id {" "		if false && err == nil && served.ID != id {"
  mut "the sender kind of a person is wrong"                 $R "m.Sender = SenderDTO{Kind: \"agent\"}" "m.Sender = SenderDTO{Kind: \"contact\"}"
  ;;
h4)
  echo "== H4: journal, sequencer, events API, SSRF, signature, delivery"
  T_PKGS="./internal/external/adapters ./internal/external/delivery ./internal/external/netpolicy"; T_NEED=3
  T_RUN="TestJournal|TestSequencer|TestProducers|TestEventsAPI|TestBootstrap|TestWebhook|TestSignature|TestVerify|TestBackoff|TestBlockedIP|TestValidateURL|TestClient"
  J=000137_domain_events.up.sql; WH=000138_webhooks.up.sql
  EV=internal/external/adapters/events_http.go; WK=internal/external/delivery/worker.go; SG=internal/external/delivery/sign.go; NP=internal/external/netpolicy/ssrf.go; WA=internal/external/adapters/webhook_admin_http.go
  sqlmut "a fact is numbered below an open transaction"  $J "WHERE e.seq IS NULL AND e.xid < horizon.h ORDER BY e.xid, e.ord LIMIT p_limit" "WHERE e.seq IS NULL ORDER BY e.xid, e.ord LIMIT p_limit"
  sqlmut "facts are numbered out of transaction order"   $J "ORDER BY e.xid, e.ord LIMIT p_limit)
    SELECT tenant_id, array_agg(id ORDER BY xid, ord) AS ids FROM due" "LIMIT p_limit)
    SELECT tenant_id, array_agg(id) AS ids FROM due"
  sqlmut "two sequencers run at once"                    $J "  IF NOT pg_try_advisory_xact_lock(hashtextextended('omnira.domain_events.sequencer', 0)) THEN
    RETURN 0;
  END IF;" ""
  sqlmut "a fact is recorded without a live installation" $J "  IF NOT EXISTS (SELECT 1 FROM external_app_installations WHERE tenant_id = p_tenant AND status <> 'revoked') THEN
    RETURN;
  END IF;" ""
  sqlmut "a fact of a hidden conversation is recorded"   $J "  IF p_conversation IS NOT NULL AND NOT external_conversation_visible(p_tenant, p_conversation) THEN
    RETURN;
  END IF;" ""
  sqlmut "the visibility rule forgets spam contacts"     $J "AND vc.kind NOT IN ('internal', 'spam')))
\$\$;" "AND vc.kind NOT IN ('internal')))
\$\$;"
  sqlmut "the message text travels in the fact"          $J "jsonb_build_object('direction', NEW.direction, 'type', NEW.message_type, 'status', NEW.status));" "jsonb_build_object('direction', NEW.direction, 'type', NEW.message_type, 'status', NEW.status, 'body', NEW.body));"
  sqlmut "the status receipt fact is lost"               $J "FOR EACH ROW WHEN (NEW.status IS DISTINCT FROM OLD.status) EXECUTE FUNCTION domain_events_message_status();" "FOR EACH ROW WHEN (false) EXECUTE FUNCTION domain_events_message_status();"
  sqlmut "retention drops facts that are not a prefix"   $J "DELETE FROM domain_events WHERE tenant_id = t.tenant_id AND seq IS NOT NULL AND seq <= cut;" "DELETE FROM domain_events WHERE tenant_id = t.tenant_id AND seq IS NOT NULL AND occurred_at < now() - p_keep;"
  sqlmut "retention does not move min_seq"               $J "UPDATE domain_event_cursors SET min_seq = cut + 1, updated_at = now() WHERE tenant_id = t.tenant_id AND min_seq < cut + 1;" "SELECT 1;"
  sqlmut "retention shorter than an hour is accepted"    $J "IF p_keep < interval '1 hour' THEN RAISE EXCEPTION 'retention shorter than one hour refused'; END IF;" ""
  mut "the cursor from the future is accepted"           $EV "		if after > head {" "		if false {"
  mut "a cursor older than the kept facts is accepted"   $EV "		if after+1 < min {" "		if false && after+1 < min {"
  mut "an invisible fact is delivered"                   $EV "			if !visible {
				continue
			}" "			_ = visible"
  mut "the cursor does not advance past filtered facts"  $EV "		out.NextCursor = strconv.FormatInt(last, 10)" "		out.NextCursor = strconv.FormatInt(after, 10)"
  mut "a negative after is accepted"                     $EV "	if err != nil || after < 0 {" "	if err != nil {"
  mut "another company's facts are readable (the journal predicate AND the visibility rule are repeated defences)" $EV "			   WHERE e.tenant_id = \`+t+\` AND e.seq IS NOT NULL" "			   WHERE e.seq IS NOT NULL" "		visConv := v.Conversation(\"c\", &args)" "		visConv := \"true\"; _ = v.Conversation(\"c\", &args)"
  sqlmut "a fact is fanned out to a revoked installation" $WH "JOIN external_app_installations i ON i.tenant_id = s.tenant_id AND i.id = s.installation_id AND i.status = 'active'
     WHERE s.status = 'active'" "JOIN external_app_installations i ON i.tenant_id = s.tenant_id AND i.id = s.installation_id
     WHERE s.status = 'active'"
  sqlmut "a delivery of a revoked installation is claimed" $WH "  UPDATE webhook_deliveries d SET status = 'cancelled', last_error_class = 'revoked'
   WHERE d.status = 'pending' AND d.next_attempt_at <= now()" "  UPDATE webhook_deliveries d SET status = 'cancelled', last_error_class = 'revoked'
   WHERE false AND d.status = 'pending' AND d.next_attempt_at <= now()"
  sqlmut "the same fact is delivered twice"               $WH "  UNIQUE (subscription_id, event_seq)," "  UNIQUE (id),"
  sqlmut "a fact that stopped being visible is dead, not cancelled" $WH "WHEN p_error_class = 'not_visible' THEN 'cancelled' " ""
  sqlmut "the rotation keeps no previous secret"         $WH "previous_key_id = key_id, previous_secret_ciphertext = secret_ciphertext, previous_valid_until = now() + p_keep," "previous_key_id = NULL, previous_secret_ciphertext = NULL, previous_valid_until = NULL,"
  sqlmut "anyone may rotate a secret"                    $WH "  IF NOT actor_has_permission(p_tenant, current_user_id(), 'integration.manage') THEN RETURN FALSE; END IF;" ""
  sqlmut "the sealed secret is readable"                 $WH "GRANT SELECT (id, tenant_id, installation_id, url," "GRANT SELECT (secret_ciphertext, id, tenant_id, installation_id, url,"
  mut "the signature ignores the timestamp"              $SG "	m.Write([]byte(strconv.FormatInt(timestamp, 10)))
	m.Write([]byte{'.'})" "	m.Write([]byte{'.'})"
  mut "an old call verifies"                             $SG "	if d > MaxClockSkew || d < -MaxClockSkew {
		return false
	}" "	_ = d"
  mut "backoff has no ceiling"                           $SG "	if d > time.Hour {
		d = time.Hour
	}" ""
  mut "the signature header drops the previous secret"   $SG "	if len(previous) > 0 {" "	if false {"
  mut "the worker does not re-ask visibility"            $WK "	if !visible {" "	if false && !visible {"
  mut "a redirect counts as success"                     $WK "	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		w.fail(ctx, c, resp.StatusCode, \"redirect\")" "	case false:
		w.fail(ctx, c, resp.StatusCode, \"redirect\")"
  mut "any status counts as success"                     $WK "	case resp.StatusCode >= 200 && resp.StatusCode < 300:" "	case true:"
  mut "the retry window never ends"                      $WK "	if next.Sub(first) > w.Window {" "	if false && next.Sub(first) > w.Window {"
  mut "the previous secret never expires"                $WK "c.prevValidUntil.After(w.now())" "true"
  mut "the delivery id changes at every attempt"         $WK "	req.Header.Set(\"X-Omnira-Delivery-ID\", c.deliveryID.String())" "	req.Header.Set(\"X-Omnira-Delivery-ID\", uuid.NewString())"
  mut "private addresses are dialed"                     $NP "				if !o.Allow(ip) {
					return nil, ErrBlockedAddress
				}" "				_ = ip"
  mut "redirects are followed"                           $NP "		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }," ""
  mut "the metadata address is allowed (prefix list AND IsLinkLocalUnicast)" $NP "		\"169.254.0.0/16\",  // link-local, AWS/GCP/Azure metadata
" "" " || addr.IsLinkLocalUnicast()" ""
  mut "IPv4-mapped IPv6 is not unmapped"                 $NP "	addr = addr.Unmap()" ""
  mut "any port is allowed"                              $NP "			if !o.Ports[port] {
				return nil, fmt.Errorf(\"%w: port %s\", ErrBadURL, port)
			}" ""
  mut "the webhook URL is not validated at save"         $WA "	if err := netpolicy.ValidateURL(req.URL); err != nil {
		http.Error(w, \"the webhook URL must be https, on a public address, with no credentials, on port 443 or 8443\", http.StatusBadRequest)
		return
	}
	var count int" "	var count int"
  mut "more than five subscriptions are allowed"         $WA "	if count >= maxSubscriptionsPerInstallation {" "	if false {"
  sqlmut "retention removes a recent fact numbered below an old one" $J "SELECT COALESCE(min(seq) FILTER (WHERE occurred_at >= now() - p_keep) - 1, max(seq)) INTO cut FROM domain_events WHERE tenant_id = t.tenant_id AND seq IS NOT NULL;" "SELECT max(seq) INTO cut FROM domain_events WHERE tenant_id = t.tenant_id AND seq IS NOT NULL AND occurred_at < now() - p_keep;"
  sqlmut "the delivery of a purged event waits for ever" $WH "          OR NOT EXISTS (SELECT 1 FROM domain_events e WHERE e.id = d.event_id AND e.tenant_id = d.tenant_id));" "          );"
  sqlmut "a paused subscription is claimed" $WH "       AND EXISTS (SELECT 1 FROM webhook_subscriptions s WHERE s.id = d.subscription_id AND s.tenant_id = d.tenant_id AND s.status = 'active')
     ORDER BY d.next_attempt_at" "     ORDER BY d.next_attempt_at"
  sqlmut "a late completion overwrites the current attempt" $WH "AND d.status = 'pending' AND d.attempts = p_attempt;
  GET DIAGNOSTICS n = ROW_COUNT;" "AND d.status = 'pending';
  GET DIAGNOSTICS n = ROW_COUNT;"
  sqlmut "the completion takes the delivery before the subscription" $WH "  PERFORM 1 FROM webhook_subscriptions s WHERE s.id = sub AND s.tenant_id = p_tenant FOR UPDATE;
" ""
  sqlmut "the limit of five is only a count" $WH "  PERFORM pg_advisory_xact_lock(hashtextextended('omnira.webhook_subscriptions:' || NEW.installation_id::text, 0));
" ""
  mut "the worker sends to a paused subscription" $WK "		if !*active {
			return errPaused
		}" "		_ = active"
  mut "one slow receiver holds all the others" $WK "	sem := make(chan struct{}, w.Parallel)" "	sem := make(chan struct{}, 1)"
  ;;
h5)
  echo "== H5: coordinated write commands, receipts, If-Match, transfer, finalize"
  T_PKGS="./internal/external/adapters ./internal/messages/application ./internal/messages/adapters"; T_NEED=1
  T_RUN="TestWrite|TestExternalSender|TestInsertQueuedExternal"
  W=internal/external/adapters/write_http.go; ES=internal/messages/application/external_send.go; EO=internal/messages/adapters/outbound_external.go; CS=internal/conversationcontrol/service.go
  AT=internal/attendance/application/service.go; MG=migrations/000139_external_commands.up.sql
  mut "the Idempotency-Key is optional"                 $W "		if !idempotencyKeyPattern.MatchString(key) {" "		if false && !idempotencyKeyPattern.MatchString(key) {"
  mut "If-Match is optional where it is required"       $W "		} else if spec.ifMatch == ifMatchRequired {" "		} else if false {"
  mut "the receipt is not looked up"                    $W "		if err == nil {
			if rHash != hash {" "		if false {
			if rHash != hash {"
  mut "a reused key with another payload is replayed"   $W "			if rHash != hash {
				return refuse(" "			if false {
				return refuse("
  mut "a lost race on the receipt is not detected"      $W "		if tag.RowsAffected() != 1 {
			return errReceiptRace" "		if false && tag.RowsAffected() != 1 {
			return errReceiptRace"
  mut "the receipt is not written"                      $W "VALUES (\$1,\$2,\$3,\$4,\$5,\$6,\$7,\$8,\$9,\$10::jsonb) ON CONFLICT DO NOTHING" "SELECT \$1,\$2,\$3,\$4,\$5,\$6,\$7,\$8,\$9,\$10::jsonb WHERE false ON CONFLICT DO NOTHING"
  mut "the conversation is not locked before the receipt" $W "		if _, err := h.ctl.Lock(ctx, q, in.p.TenantID, in.conv); err != nil {
			return h.fromControl(err, nil)
		}" "		_ = h"
  mut "the installation is not re-checked in the transaction" $W "		if err := RecheckPrincipal(ctx, q, in.p); err != nil {
			return err
		}" "		_ = RecheckPrincipal"
  mut "an invisible conversation is not a 404"         $W "			if errors.Is(err, pgx.ErrNoRows) {
				return errNotVisible
			}
			return err
		}
		in.queueID = queue" "			if errors.Is(err, pgx.ErrNoRows) {
				queue = nil
			} else {
				return err
			}
		}
		in.queueID = queue"
  mut "a send does not need the hold"                   $W "	if !cur.HeldBy(conversationcontrol.KindCRMApplication, in.p.InstallationID) {
		if cur.Free() {" "	if false {
		if cur.Free() {"
  mut "a send does not check the version"               $W "	if in.ifMatch == nil || *in.ifMatch != cur.Version {
		return refuse(http.StatusPreconditionFailed, \"stale_control_version\", \"The conversation changed\", \"The control version is not the current one; read the conversation again.\", &cur)
	}
	return nil" "	return nil"
  mut "a send to a closed conversation is accepted"     $W "	if cur.Status != \"open\" {
		return refuse(http.StatusConflict, \"conversation_closed\"" "	if false {
		return refuse(http.StatusConflict, \"conversation_closed\""
  mut "the outside-hours rule is ignored"               $W "&& in.queueID != nil && !in.p.AllowOutsideHours && h.d.QueueOpen != nil {" "&& false {"
  mut "a transfer may use a queue outside the allow-list" $W "		if len(in.al.QueueIDs) > 0 && !containsUUID(in.al.QueueIDs, pl.QueueID) {" "		if false {"
  mut "the message carries no origin (the audit names a person)" $W "VALUES (\$1, NULL, 'external_app.command'" "VALUES (\$1, \$1, 'external_app.command'"
  mut "the sender does not check the 24 h window"       $ES "		if _, open := SessionWindow(sc.Provider, sc.LastInboundAt, now); !open {" "		if false {"
  mut "the sender sends to a closed conversation"       $ES "	if sc.Closed {
		return nil, ErrConversationClosed
	}" ""
  mut "the sender needs no channel"                     $ES "	if sc.ConnectionID == nil || !sc.ConnectionReady || sc.ToE164 == \"\" {
		return nil, ErrChannelUnavailable
	}" ""
  mut "an unapproved template is sent"                  $ES "	if tpl == nil || tpl.Status != \"APPROVED\" || !tpl.Sendable {" "	if tpl == nil {"
  mut "the insert does not prove the hold"        $EO "c.control_kind = 'crm_application' AND c.control_holder_id = \$7 AND c.control_version = \$10" "true"
  mut "the insert ignores the control version"          $EO "AND c.control_holder_id = \$7 AND c.control_version = \$10" "AND c.control_holder_id = \$7 AND \$10 >= 0"
  mut "the insert ignores a closed conversation"        $EO "		    AND c.status = 'open'
" ""
  mut "the transfer does not need the holder"           $CS "	if !cur.HeldBy(KindCRMApplication, installationID) {
		return cur, ErrNotHolder
	}
	if ifMatch != cur.Version {
		return cur, ErrStaleVersion
	}
	if cur.Status != \"open\" {
		return cur, ErrClosed
	}
	var queueMode string" "	var queueMode string"
  mut "a transfer to an ineligible person works"        $CS "		ok, err := eligible(ctx, q, tenantID, to.QueueID, *to.UserID)
		if err != nil {
			return cur, err
		}
		if !ok {
			return cur, ErrInvalidTarget
		}" "		_ = eligible"
  mut "a transfer to a queue of another company works"  $CS "	if err := q.QueryRow(ctx, \`SELECT mode FROM queues WHERE tenant_id = \$1 AND id = \$2\`, tenantID, to.QueueID).Scan(&queueMode); err != nil {" "	if err := q.QueryRow(ctx, \`SELECT mode FROM queues WHERE id = \$2 AND \$1 IS NOT NULL\`, tenantID, to.QueueID).Scan(&queueMode); err != nil {"
  mut "the transfer to a queue keeps the hold"          $CS "			   SET queue_id = \$3, assigned_to_user_id = NULL, assigned_at = NULL,
			       control_kind = NULL, control_holder_id = NULL, control_since = NULL, control_origin = NULL, control_version = \$4," "			   SET queue_id = \$3, assigned_to_user_id = NULL, assigned_at = NULL, control_version = \$4,"
  mut "finalize is attributed to a person"             $AT "ClosedBy: nil, ClosedByInstallation: &inst,
		Source: domain.SourceExternalApp" "ClosedBy: nil, ClosedByInstallation: func() *uuid.UUID { _ = inst; return nil }(),
		Source: domain.SourceSystem"
  mut "finalize closes no local tickets"                $AT "	closed, kept, err := s.repo.CloseLocalTickets(ctx, conversationID)
	if err != nil {
		return FinalizeResult{}, err
	}
	if err := s.repo.MarkClosed(ctx, conversationID); err != nil {
		return FinalizeResult{}, err
	}
	inst := installationID" "	closed, kept := 0, 0
	if err := s.repo.MarkClosed(ctx, conversationID); err != nil {
		return FinalizeResult{}, err
	}
	inst := installationID"
  sqlmut "a receipt can be changed"                     000139_external_commands.up.sql "GRANT SELECT, INSERT ON external_command_receipts TO omnira_app;" "GRANT SELECT, INSERT, UPDATE, DELETE ON external_command_receipts TO omnira_app;"
  sqlmut "a receipt is scoped to the tenant only"       000139_external_commands.up.sql "PRIMARY KEY (tenant_id, installation_id, operation, idempotency_key)," "PRIMARY KEY (tenant_id, operation, idempotency_key),"
  ;;
*) echo "unknown phase $phase"; exit 2;;
esac
done
mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "FAIL: suite red after restore"; exit 1; }
echo "PASS: every Headless mutation was caught ($PHASES)"
