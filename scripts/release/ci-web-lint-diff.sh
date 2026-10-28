#!/usr/bin/env bash
# ci-web-lint-diff.sh — ESLint gate scoped to .ts/.tsx files actually changed in this PR/branch.
#
# Why diff-scoped: `npm run lint` had NO working configuration at all before
# this program (F2) added web/eslint.config.js — it failed immediately with
# "ESLint couldn't find a configuration file" on every invocation. Now that
# it actually runs, a first real pass over the WHOLE src/ tree finds 4 real
# errors and 43 warnings in pre-existing code unrelated to this program (see
# docs/release/IMPLEMENTATION-STATUS.md, fase F2). Blocking every future PR
# on those would violate CLAUDE.md ("não refatore partes não relacionadas")
# and the specification's own rule that legacy issues are classified, not
# used to silently block unrelated work. This gate only fails on an ESLint
# ERROR (not a warning) in a file the PR actually touched.
#
# Usage: scripts/release/ci-web-lint-diff.sh <base-ref>
set -euo pipefail

BASE_REF="${1:?usage: $0 <base-ref>}"
REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

changed=$(git diff --name-only --diff-filter=ACMR "${BASE_REF}...HEAD" -- 'web/*.ts' 'web/*.tsx')
if [ -z "$changed" ]; then
  echo "ci-web-lint-diff: no changed .ts/.tsx files under web/ against ${BASE_REF}"
  exit 0
fi

cd "$REPO_ROOT/web"
existing=()
while IFS= read -r f; do
  rel="${f#web/}"
  [ -f "$rel" ] && existing+=("$rel")
done <<<"$changed"

if [ "${#existing[@]}" -eq 0 ]; then
  echo "ci-web-lint-diff: OK (all changed files were deleted)"
  exit 0
fi

echo "ci-web-lint-diff: linting ${#existing[@]} changed file(s) against ${BASE_REF}"
npx eslint "${existing[@]}"
