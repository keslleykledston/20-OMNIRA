#!/usr/bin/env bash
# Mutation test for THE DELEGATED FINALIZE OF THE ATTENDANCE (ADR-0040 phase 05b, migration 116): weakens each rule of the Hub write path and of the attendance rules
# a delegate runs in turn; the real-Postgres test must go red every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubfinmut-$$; DB=omnira_test_finmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/hub/adapters/delegated_writes.go internal/attendance/application/service.go internal/attendance/adapters/http.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=finmut-$$" \
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
    go test -count=1 -p 1 -run 'TestDelegatedFinalize' \
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
# NOT mutants (equivalent by construction):
#   - the row lock mode in replying.Act (FOR UPDATE): a second writer is serialized by the same lock inside the attendance rules (LockConversation);
#   - the audit text rule: ids, counts and enums only, by construction of the metadata map (the test looks for the customer text in the trail).
DW=internal/hub/adapters/delegated_writes.go; AS=internal/attendance/application/service.go; AH=internal/attendance/adapters/http.go

mut "a delegate who does not hold the conversation may finalize it" $AS 'if facts.AssignedTo == nil || *facts.AssignedTo != agent {' 'if false && (facts.AssignedTo == nil || *facts.AssignedTo != agent) {'
mut "the closure does not name the agent" $AS '		ID: uuid.New(), TenantID: tc.TenantID, ConversationID: in.ConversationID, ContactID: *facts.ContactID, ClosedBy: &by,
		Source: domain.SourceAgent,' '		ID: uuid.New(), TenantID: tc.TenantID, ConversationID: in.ConversationID, ContactID: *facts.ContactID, ClosedBy: nil,
		Source: domain.SourceAgent,'
mut "the closure is recorded as a supervisor's" $AS '		Source: domain.SourceAgent, Reason: in.Reason, Note: in.Note, Summary: in.Summary, SummaryTruth: in.SummaryTruth,
		LocalTicketsClosed: closed, TicketsKept: kept, CreatedAt: now,
	}
	if err := s.repo.InsertClosure(ctx, closure); err != nil {
		return FinalizeResult{}, err
	}
	items := make([]domain.FollowUp, 0, len(in.FollowUps))
	for _, f := range in.FollowUps {
		cid := closure.ID
		items = append(items, domain.FollowUp{
			ID: uuid.New(), TenantID: tc.TenantID, ContactID: *facts.ContactID, ConversationID: in.ConversationID, ClosureID: &cid,
			Kind: f.Kind, Text: f.Text, OwnerUserID: f.OwnerUserID, DueAt: f.DueAt, Status: domain.StatusOpen,
			Truth: domain.TruthAgentConfirmed, CreatedBy: &by, CreatedAt: now,
		})
	}
	if err := s.repo.InsertFollowUps(ctx, items); err != nil {
		return FinalizeResult{}, err
	}
	return FinalizeResult{Closure: closure, FollowUps: items, Changed: true}, nil
}

// FinalizeBySystem' '		Source: domain.SourceSupervisor, Reason: in.Reason, Note: in.Note, Summary: in.Summary, SummaryTruth: in.SummaryTruth,
		LocalTicketsClosed: closed, TicketsKept: kept, CreatedAt: now,
	}
	if err := s.repo.InsertClosure(ctx, closure); err != nil {
		return FinalizeResult{}, err
	}
	items := make([]domain.FollowUp, 0, len(in.FollowUps))
	for _, f := range in.FollowUps {
		cid := closure.ID
		items = append(items, domain.FollowUp{
			ID: uuid.New(), TenantID: tc.TenantID, ContactID: *facts.ContactID, ConversationID: in.ConversationID, ClosureID: &cid,
			Kind: f.Kind, Text: f.Text, OwnerUserID: f.OwnerUserID, DueAt: f.DueAt, Status: domain.StatusOpen,
			Truth: domain.TruthAgentConfirmed, CreatedBy: &by, CreatedAt: now,
		})
	}
	if err := s.repo.InsertFollowUps(ctx, items); err != nil {
		return FinalizeResult{}, err
	}
	return FinalizeResult{Closure: closure, FollowUps: items, Changed: true}, nil
}

// FinalizeBySystem'
mut "a follow-up owner who is not a member of the instance is accepted" $AS '		if !ok {
			return FinalizeResult{}, errors.Join(domain.ErrInvalid, errors.New("a follow-up owner is not an active member of this tenant"))
		}
	}
	closed, kept, err := s.repo.CloseLocalTickets(ctx, in.ConversationID)
	if err != nil {
		return FinalizeResult{}, err
	}
	if err := s.repo.MarkClosed(ctx, in.ConversationID); err != nil {
		return FinalizeResult{}, err
	}
	by := agent' '		if false && !ok {
			return FinalizeResult{}, errors.Join(domain.ErrInvalid, errors.New("a follow-up owner is not an active member of this tenant"))
		}
	}
	closed, kept, err := s.repo.CloseLocalTickets(ctx, in.ConversationID)
	if err != nil {
		return FinalizeResult{}, err
	}
	if err := s.repo.MarkClosed(ctx, in.ConversationID); err != nil {
		return FinalizeResult{}, err
	}
	by := agent'
mut "a repeated finalize closes (and audits) again" $AS '	if facts.Status == "closed" {
		existing, err := s.repo.ClosureByConversation(ctx, in.ConversationID)
		if err != nil {
			return FinalizeResult{}, err
		}
		res := FinalizeResult{Closure: existing}
		if existing != nil {
			if res.FollowUps, err = s.repo.FollowUpsOfClosure(ctx, existing.ID); err != nil {
				return FinalizeResult{}, err
			}
		}
		return res, nil
	}
	for _, f := range in.FollowUps {
		if f.OwnerUserID == nil {
			continue
		}
		ok, err := s.repo.IsActiveMember(ctx, *f.OwnerUserID)
		if err != nil {
			return FinalizeResult{}, err
		}
		if !ok {
			return FinalizeResult{}, errors.Join(domain.ErrInvalid, errors.New("a follow-up owner is not an active member of this tenant"))
		}
	}
	closed, kept, err := s.repo.CloseLocalTickets(ctx, in.ConversationID)
	if err != nil {
		return FinalizeResult{}, err
	}
	if err := s.repo.MarkClosed(ctx, in.ConversationID); err != nil {
		return FinalizeResult{}, err
	}
	by := agent' '	if false && facts.Status == "closed" {
		existing, err := s.repo.ClosureByConversation(ctx, in.ConversationID)
		if err != nil {
			return FinalizeResult{}, err
		}
		res := FinalizeResult{Closure: existing}
		if existing != nil {
			if res.FollowUps, err = s.repo.FollowUpsOfClosure(ctx, existing.ID); err != nil {
				return FinalizeResult{}, err
			}
		}
		return res, nil
	}
	for _, f := range in.FollowUps {
		if f.OwnerUserID == nil {
			continue
		}
		ok, err := s.repo.IsActiveMember(ctx, *f.OwnerUserID)
		if err != nil {
			return FinalizeResult{}, err
		}
		if !ok {
			return FinalizeResult{}, errors.Join(domain.ErrInvalid, errors.New("a follow-up owner is not an active member of this tenant"))
		}
	}
	closed, kept, err := s.repo.CloseLocalTickets(ctx, in.ConversationID)
	if err != nil {
		return FinalizeResult{}, err
	}
	if err := s.repo.MarkClosed(ctx, in.ConversationID); err != nil {
		return FinalizeResult{}, err
	}
	by := agent'
mut "the conversation of the body overrides the persisted item's" $DW 'in.ConversationID = t.Item.ConversationID // the conversation of the persisted item, never a body field' '_ = t.Item.ConversationID'
mut "an idempotent repeat is audited again" $DW '			if !out.Changed {
				return nil, nil
			}' '			if false && !out.Changed {
				return nil, nil
			}'
mut "unknown fields in the body are accepted" $DW '		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			httpError(w, "invalid request body", http.StatusBadRequest)' '		if err := dec.Decode(&in); err != nil {
			httpError(w, "invalid request body", http.StatusBadRequest)'
mut "the route asks for a key every delegate has instead of conversation.finalize" $AH 'tenancyadapters.Delegable("conversation.finalize",' 'tenancyadapters.Delegable("conversation.read",'
sqlmut "the key has no data domain (not delegable)" 000116_hub_delegated_finalize.up.sql "INSERT INTO permission_domains (permission_key, domain, need) VALUES ('conversation.finalize', 'conversation', 'write');" "SELECT 1;"
echo "PASS: all $N mutants killed"
