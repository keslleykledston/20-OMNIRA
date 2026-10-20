#!/usr/bin/env bash
# test-manifest-verifier.sh — exercises the release manifest verifier
# (apps/releasectl validate-manifest, backed by internal/platformrelease/
# domain.ParseManifest) against good/tampered/malicious fixtures (F3's own
# explicit requirement: "Criar verificadores locais com fixtures boas/
# alteradas/maliciosas"). This is a second, independent layer of evidence
# on top of the Go unit tests in internal/platformrelease/domain/
# manifest_test.go — those test the parser's internals directly; this
# exercises the actual CLI binary a human or a workflow would run.
#
# Usage: scripts/release/test-manifest-verifier.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

fail=0
check() {
  local fixture="$1" expect="$2" # expect: pass|fail
  local out
  if out=$(go run ./apps/releasectl/cmd/releasectl validate-manifest --file "release/fixtures/${fixture}" 2>&1); then
    result=pass
  else
    result=fail
  fi
  if [ "$result" = "$expect" ]; then
    echo "OK   ${fixture} (expected ${expect}, got ${result})"
  else
    echo "FAIL ${fixture}: expected ${expect}, got ${result} — output: ${out}" >&2
    fail=1
  fi
}

check manifest-good.json pass
check manifest-tampered-movable-tag.json fail
check manifest-malicious-unknown-field.json fail
check manifest-malicious-case-variant-key.json fail

if [ "$fail" -ne 0 ]; then
  echo "test-manifest-verifier: FAILED" >&2
  exit 1
fi
echo "test-manifest-verifier: OK (4/4 fixtures behaved as expected)"
