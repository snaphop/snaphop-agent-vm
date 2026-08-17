#!/usr/bin/env bash
# The minimum verification required before handing work off (AGENTS.md §4):
# formatting, vet, lint, and the unit tests. CI runs the same script, so a green
# run here means a green run there.
#
# golangci-lint is skipped with a warning when it is not installed, because a
# missing linter should not look like passing code.
set -euo pipefail

cd "$(dirname "$0")/.."

fail=0
step() { printf '\n== %s\n' "$1"; }

step "gofmt"
unformatted="$(gofmt -l . || true)"
if [ -n "$unformatted" ]; then
  echo "these files are not gofmt-clean:"
  echo "$unformatted"
  echo "fix with: gofmt -w ."
  fail=1
else
  echo "clean"
fi

step "go vet"
go vet ./... || fail=1

step "golangci-lint"
if command -v golangci-lint >/dev/null 2>&1; then
  # Report every finding: the defaults cap repeats per linter, which hides the
  # tail of a backlog and makes a partial fix look complete.
  golangci-lint run --max-same-issues=0 --max-issues-per-linter=0 || fail=1
else
  echo "WARNING: golangci-lint is not installed; lint was NOT run."
  echo "Install it from https://golangci-lint.run/ before claiming a clean run."
fi

step "go test"
go test ./... || fail=1

if [ "$fail" -ne 0 ]; then
  printf '\nchecks failed\n'
  exit 1
fi

printf '\nall checks passed\n'
printf 'Integration tests are separate and need a KVM host:\n'
printf '  go test -tags integration ./test/integration/...\n'
