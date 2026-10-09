#!/usr/bin/env bash
# Mutation test for the Access panel and its neighbours (ADR-0039): weakens the access service, the provisioning guard and
# grant rules, the inbox company filter, the attachment-send gate and the single-instance invitation rule (migration 101);
# the real-Postgres suite must go red every time and green on the original.
# Throwaway database only.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubaccessmut-$$; DB=omnira_test_accessmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/hub/access/service.go internal/hub/provisioning/service.go internal/hub/adapters/access_http.go internal/hub/adapters/http.go internal/hub/adapters/postgres.go internal/messages/application/attachment.go internal/tenancy/adapters/invitations_http.go internal/tenancy/adapters/team_http.go internal/messages/adapters/http.go internal/hub/access/invitations.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=accessmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)

mkdb() { # mkdb <migrations dir>
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
  docker run --rm --network "container:$NAME" -v "$1:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
}
run() { # the host toolchain with its warm module and build caches (a cold container recompiles everything per mutant)
  OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false \
    OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    go test -count=1 -run 'TestAccess|TestHubInbox_CompanyFilter|TestHubInbox_AFutureGrant|TestProvisioning_GrantingAgain|TestInvitationRefuses|TestTeamReactivation|TestTwoReactivations|TestTwoAdminDemotions|TestSendMediaHonours|TestSendingAnUploadedFile' ./internal/hub/adapters ./internal/hub/provisioning ./internal/tenancy/adapters ./internal/messages/application ./internal/messages/adapters 2>&1
}
# NOTE: exit status of a pipeline ending in grep is grep's, so the verdict is read from the output.
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge 5 ]; }

mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "$out" | tail -20; echo "FAIL: baseline is red"; exit 1; }
echo "== baseline green"

killed() { # killed <name> <output>
  if verdict_green "$2"; then echo "FAIL: mutation '$1' SURVIVED"; exit 1; fi
  echo "$2" | grep -q "^FAIL" || { echo "$2" | tail -5; echo "FAIL: mutation '$1' was inconclusive"; exit 1; }
  echo "   killed: $1 ($(echo "$2" | grep -c -- '--- FAIL') failing tests)"
}
N=0 # SKIP=<n> resumes after the first n mutants (a hung docker run is not a verdict; rerun with SKIP to continue)
mut() { # mut <name> <file> <from> <to>
  N=$((N+1)); [ "$N" -le "${SKIP:-0}" ] && return 0
  python3 - "$2" "$WORK/orig/$2" "$3" "$4" <<'PY'
import sys
p,orig,f,t=sys.argv[1:5]
s=open(orig).read()
assert f in s, "mutation target not found: "+f
open(p,'w').write(s.replace(f,t,1))
PY
  killed "$1" "$(run || true)"
  cp "$WORK/orig/$2" "$2"
}
S=internal/hub/access/service.go; P=internal/hub/provisioning/service.go; H=internal/hub/adapters/access_http.go; L=internal/hub/adapters/http.go; D=internal/hub/adapters/postgres.go; A=internal/messages/application/attachment.go; I=internal/tenancy/adapters/invitations_http.go; T=internal/tenancy/adapters/team_http.go; W=internal/messages/adapters/http.go
mut "the access service lets anybody in"                  $S "		ok, err := lockAuthority(c, q, hub, actor)
		if err != nil {
			return err
		}
		if !ok {" "		ok, err := lockAuthority(c, q, hub, actor)
		if err != nil {
			return err
		}
		if false {"
mut "the access service ignores a suspended hub"          internal/hub/access/invitations.go "AND EXISTS (SELECT 1 FROM service_hubs WHERE id = \$1 AND status = 'active')" "AND true"
mut "the handler lets a non-admin through"                $H "	if !admin {
		httpError(w, \"not found\", http.StatusNotFound)" "	if false {
		httpError(w, \"not found\", http.StatusNotFound)"
mut "the grant guard never refuses"                       $P "	if !ok {
		return ErrForbidden
	}
	return nil
}" "	if false {
		return ErrForbidden
	}
	return nil
}"
mut "the guard is skipped for acting people"              $P "	actor := actorOf(ctx)
	if actor == uuid.Nil {
		return nil
	}
	var ok bool" "	actor := actorOf(ctx)
	if true {
		return nil
	}
	var ok bool"
mut "a person can be made hub admin from the panel"       $P "if actorOf(ctx) != uuid.Nil && role != RoleAgent {" "if false {"
mut "a hub admin can be demoted from the panel"           $P "if prev != nil && *prev == RoleAdmin && actorOf(c) != uuid.Nil {" "if false {"
mut "a hub admin can be removed from the panel"           $P "if found && key == RoleAdmin {" "if false \&\& found \&\& key == RoleAdmin {"
mut "audit loses the acting person"                       $P "		actor = &a
		meta[\"via\"] = \"omnira-access-panel\"" "		_ = a
		meta[\"via\"] = \"omnira-access-panel\""
mut "re-granting revives a revoked grant silently"        $P "				widens := exStatus != \"active\" ||" "				widens := false && exStatus != \"active\" ||"
mut "re-granting lifts a validity silently"               $P "(exUntil != nil && (spec.ValidUntil == nil || spec.ValidUntil.Truncate(time.Microsecond).After(*exUntil))) ||" "false ||"
mut "re-granting upgrades read-only to reply silently"    $P "(!exReply && spec.CanReply)" "false"
mut "'none' does not revoke"                              $S "		err := s.prov.RevokeGrant(pctx, hub, tenant, user)" "		var err error"
mut "the matrix cell cannot renew an explicit change"     $S "CanReply: mode == \"reply\", Renew: true})" "CanReply: mode == \"reply\"})"
mut "the last administrator can be removed"               $S "		if len(admins) < 2 {" "		if false {"
mut "a person who is not an admin can be 'removed'"       $S "		if !isAdmin {" "		if false && !isAdmin {"
mut "an instance outside the hub can be administered"     $S "	if !ok {
		return fmt.Errorf(\"%w: no active instance %s in this hub\", ErrNotFound, tenant)
	}" "	_ = ok"
mut "unknown and inactive e-mails answer differently"     $S "	if n != 1 {
		return uuid.Nil, invalid(\"no eligible account for that e-mail (the person must already have an OMNIRA account)\")" "	if n == 0 {
		return uuid.Nil, invalid(\"no account for that e-mail\")"
mut "the overview lists every agent as single-instance"   $S "			out.Agents[i].Instances = len(seen)" "			out.Agents[i].Instances = 1"
mut "the inbox company filter is ignored"                 $D "	if len(onlyTenants) > 0 {" "	if false {"
mut "offered companies are not limited to the caller"     $L "WHERE g.hub_id = \$1 AND g.user_id = \$2 AND g.status = 'active'" "WHERE g.hub_id = \$1 AND \$2::uuid IS NOT NULL AND g.status = 'active'"
mut "offered companies include revoked grants"            $L "WHERE g.hub_id = \$1 AND g.user_id = \$2 AND g.status = 'active' AND g.valid_from <= now() AND (g.valid_until IS NULL OR g.valid_until > now())" "WHERE g.hub_id = \$1 AND g.user_id = \$2"
mut "the filter accepts a tenant_id selector"             $L "	if r.URL.Query().Has(\"tenant_id\") {" "	if false {"
mut "an uploaded file leaves after the switch is off"     $A "	if s.entitled != nil {" "	if false {"
mut "invitation to someone elsewhere is accepted"         $I "	if h.worksElsewhere(r.Context(), q, tc.TenantID, email) {
		http.Error(w, errOtherInstanceMessage, http.StatusConflict)
		return
	}
" ""
mut "a failed lookup counts as 'not elsewhere'"           $I "	if err := q.QueryRow(ctx, \`SELECT person_works_in_other_instance(\$1, \$2)\`, tenantID, email).Scan(&yes); err != nil {
		return true
	}" "	if err := q.QueryRow(ctx, \`SELECT person_works_in_other_instance(\$1, \$2)\`, tenantID, email).Scan(&yes); err != nil {
		return false
	}"

mut "a deactivated hub admin still passes the panel guard" internal/hub/access/invitations.go "AND EXISTS (SELECT 1 FROM users WHERE id = \$2 AND status = 'active')" "AND true"
mut "a deactivated hub admin still passes the grant guard" $P "AND EXISTS (SELECT 1 FROM users WHERE id = \$2 AND status = 'active')" "AND true"
mut "the panel reads a hub role without locking the row"  $P "WHERE hub_id = \$1 AND user_id = \$2 FOR UPDATE\`, hub, user).Scan(&roleID)" "WHERE hub_id = \$1 AND user_id = \$2\`, hub, user).Scan(&roleID)"
mut "the media route skips the attachments switch"        $W "res, err = h.att.SendMedia(" "res, err = h.svc.SendMedia("
mut "reactivating a membership ignores the single-instance rule" $T "; err != nil || elsewhere {" "; err != nil && elsewhere {"
mut "reactivation is not serialized per person"           $T "		if err := lockPerson(r.Context(), q, targetUser); err != nil {
			http.Error(w, \"failed to update membership\", http.StatusInternalServerError)
			return
		}
" ""
mut "invitation acceptance is not serialized per person"  $I "		if err := lockPerson(ctx, q, principal.UserID); err != nil {
			return err
		}
" ""
mut "offered companies include grants that have not started" $L "AND g.status = 'active' AND g.valid_from <= now() AND" "AND g.status = 'active' AND"

sqlmut() { # sqlmut <name> <migration file> <from> <to>
  N=$((N+1)); [ "$N" -le "${SKIP:-0}" ] && return 0
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
M=000101_person_works_in_other_instance.up.sql
# by e-mail (invitations)
sqlmut "e-mail variant: anyone may ask"                    $M "      public.is_system_admin()
      OR EXISTS (
        SELECT 1 FROM public.memberships m
        JOIN public.role_permissions rp ON rp.role_id = m.role_id
        WHERE m.tenant_id = p_tenant_id AND m.user_id = public.current_user_id()
          AND m.status = 'active' AND rp.permission_key = 'membership.manage'
      )
    ) THEN false
    ELSE EXISTS (" "      true OR public.is_system_admin()
      OR EXISTS (
        SELECT 1 FROM public.memberships m
        JOIN public.role_permissions rp ON rp.role_id = m.role_id
        WHERE m.tenant_id = p_tenant_id AND m.user_id = public.current_user_id()
          AND m.status = 'active' AND rp.permission_key = 'membership.manage'
      )
    ) THEN false
    ELSE EXISTS ("
sqlmut "e-mail variant: elsewhere includes the same company" $M "m2.user_id = u.id AND m2.status = 'active' AND m2.tenant_id <> p_tenant_id" "m2.user_id = u.id AND m2.status = 'active'"
sqlmut "e-mail variant: hub agents do not count"           $M "OR EXISTS (SELECT 1 FROM public.hub_memberships hm WHERE hm.user_id = u.id)" ""
sqlmut "e-mail variant: inactive memberships count"        $M "m2.user_id = u.id AND m2.status = 'active'" "m2.user_id = u.id"
# by user (membership reactivation)
sqlmut "by-user variant: anyone may ask"                   $M "    ) THEN false
    ELSE (
      EXISTS (SELECT 1 FROM public.memberships m2 WHERE m2.user_id = p_user_id" "    ) AND false THEN false
    ELSE (
      EXISTS (SELECT 1 FROM public.memberships m2 WHERE m2.user_id = p_user_id"
sqlmut "by-user variant: elsewhere includes the same company" $M "m2.user_id = p_user_id AND m2.status = 'active' AND m2.tenant_id <> p_tenant_id" "m2.user_id = p_user_id AND m2.status = 'active'"
sqlmut "by-user variant: inactive memberships count"       $M "m2.user_id = p_user_id AND m2.status = 'active'" "m2.user_id = p_user_id"
sqlmut "by-user variant: hub agents do not count"          $M "OR EXISTS (SELECT 1 FROM public.hub_memberships hm WHERE hm.user_id = p_user_id)" ""
sqlmut "by-user variant: no permission required"           $M "AND m.status = 'active' AND rp.permission_key = 'membership.manage'
      )
    ) THEN false
    ELSE (" "AND m.status = 'active'
      )
    ) THEN false
    ELSE ("
mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "$out" | tail -30; echo "FAIL: suite red after restore"; exit 1; }
echo "PASS: every access-panel mutation was caught"
