#!/usr/bin/env bash
# Fails when a test was skipped without a written reason.
#
# Usage: check.sh <go-test-json-report> <allowlist>
#
# A green run proves nothing about a test that did not run. Every test that
# reports "skip" in the go test -json stream must be listed in the job's
# allowlist as "<package> <TestName> # <why it cannot run in this job>".
# The check also fails on a listed test that no longer skips, so the list
# can only shrink and never hides a test that quietly came back.
set -euo pipefail
report="${1:?usage: check.sh <report.json> <allowlist>}"
allow="${2:?usage: check.sh <report.json> <allowlist>}"

skipped=$(jq -r 'select(.Action == "skip" and .Test != null) | "\(.Package) \(.Test)"' "$report" | sort -u)
allowed=$(grep -v '^\s*#' "$allow" | grep -v '^\s*$' | sed 's/\s*#.*$//' | sort -u || true)

echo "tests run: $(jq -r 'select(.Action == "pass" or .Action == "fail") | select(.Test != null) | "\(.Package) \(.Test)"' "$report" | sort -u | wc -l)"
echo "tests skipped: $(printf '%s' "$skipped" | grep -c . || true)"

unexpected=$(comm -23 <(printf '%s\n' "$skipped" | grep .) <(printf '%s\n' "$allowed" | grep .) || true)
stale=$(comm -13 <(printf '%s\n' "$skipped" | grep .) <(printf '%s\n' "$allowed" | grep .) || true)

failed=0
if [ -n "$unexpected" ]; then
  echo "::error::tests skipped without a reason in $allow:"
  printf '%s\n' "$unexpected" | while read -r pkg name; do
    reason=$(jq -r --arg p "$pkg" --arg t "$name" 'select(.Package == $p and .Test == $t and .Action == "output") | .Output' "$report" | grep -m1 -E '\.go:[0-9]+: ' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' || true)
    echo "  $pkg $name  ($reason)"
  done
  failed=1
fi
if [ -n "$stale" ]; then
  echo "::error::listed in $allow but no longer skipped - remove them:"
  printf '  %s\n' "$stale"
  failed=1
fi
exit "$failed"
