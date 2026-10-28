#!/usr/bin/env bash
# ci-go-fmt-diff.sh — gofmt gate scoped to files actually changed in this PR/branch.
#
# Why diff-scoped and not whole-repo: as of this writing `gofmt -l .` on
# main finds 42 pre-existing unformatted files (see
# docs/release/IMPLEMENTATION-STATUS.md, fase F2) — legacy drift unrelated
# to this program. A whole-repo gate would fail CI on every PR from day
# one, for files the PR never touched; a diff-scoped gate blocks only what
# a PR actually introduces or worsens, and never asks an unrelated PR to
# fix 42 strangers' files first (CLAUDE.md: "não refatore partes não
# relacionadas"). The 42 pre-existing files are a separate, tracked cleanup
# item, not something this gate silently hides — ci-main.yml reports their
# count on every push to main so the drift stays visible without blocking.
#
# Usage: scripts/release/ci-go-fmt-diff.sh <base-ref>
#   base-ref: what to diff against (e.g. origin/main, or a merge-base SHA).
#             In GitHub Actions this is the PR's base SHA.
set -euo pipefail

BASE_REF="${1:?usage: $0 <base-ref>}"

changed=$(git diff --name-only --diff-filter=ACMR "${BASE_REF}...HEAD" -- '*.go')
if [ -z "$changed" ]; then
  echo "ci-go-fmt-diff: no changed .go files against ${BASE_REF}"
  exit 0
fi

echo "ci-go-fmt-diff: checking $(echo "$changed" | wc -l | tr -d ' ') changed .go file(s) against ${BASE_REF}"
# Only files that still exist (a deleted file has nothing to format).
existing=()
while IFS= read -r f; do
  [ -f "$f" ] && existing+=("$f")
done <<<"$changed"

if [ "${#existing[@]}" -eq 0 ]; then
  echo "ci-go-fmt-diff: OK (all changed .go files were deleted)"
  exit 0
fi

dirty=$(gofmt -l "${existing[@]}")
if [ -n "$dirty" ]; then
  echo "ci-go-fmt-diff: the following changed files are not gofmt-clean:" >&2
  echo "$dirty" >&2
  echo "ci-go-fmt-diff: run 'gofmt -w <file>' on each and commit again" >&2
  exit 1
fi
echo "ci-go-fmt-diff: OK"
