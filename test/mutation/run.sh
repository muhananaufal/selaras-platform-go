#!/usr/bin/env bash
# Mutation testing gate: every package in baseline.txt is mutated with
# gremlins, and its LIVED and NOT COVERED counts may not exceed the baseline.
#
# Counts, not gremlins' efficacy percentage, and a run with any timed-out
# mutant fails as inconclusive: a timed-out mutant is neither killed nor
# lived, so a count taken with timeouts can look better than it is.
# .gremlins.yaml raises the timeout so that does not happen on a normal run.
# gremlins' own thresholds are not used: in v0.6.0 the --threshold-* flags
# are silently ignored (only a config file enforces them), which would make
# a gate that can never fail. docs/runbook/mutation-testing.md has the
# measurements behind each choice.
#
# Usage: bash test/mutation/run.sh [workers]   (needs gremlins and jq)
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKERS="${1:-0}"
OUT="$(mktemp -d)"
trap 'rm -rf "$OUT"' EXIT
cd "$ROOT" || exit 1

failed=0
while read -r pkg lived_max nc_max; do
  case "$pkg" in ''|'#'*) continue ;; esac
  report="$OUT/$(echo "$pkg" | tr / -).json"
  if ! gremlins unleash "./$pkg" --config .gremlins.yaml --workers "$WORKERS" -o "$report" >"$OUT/log" 2>&1; then
    echo "FAIL  $pkg: gremlins did not complete"; sed 's/^/      /' "$OUT/log" | tail -20; failed=1; continue
  fi
  count() { jq --arg s "$1" '[.files[].mutations[] | select(.status == $s)] | length' "$report"; }
  lived=$(count LIVED); nc=$(count "NOT COVERED"); killed=$(count KILLED); timed=$(count "TIMED OUT")
  line="$pkg: killed $killed, lived $lived (max $lived_max), not covered $nc (max $nc_max), timed out $timed"
  if [ "$timed" -gt 0 ]; then
    # A timed-out mutant is neither killed nor lived: the evidence is
    # incomplete, and passing on it would hide what it might have shown.
    echo "FAIL  $line - inconclusive, $timed mutant(s) timed out"; failed=1
  elif [ "$lived" -gt "$lived_max" ] || [ "$nc" -gt "$nc_max" ]; then
    echo "FAIL  $line"; failed=1
  else
    echo "ok    $line"
  fi
  if [ "$lived" -lt "$lived_max" ] || [ "$nc" -lt "$nc_max" ]; then
    echo "      lower the baseline to: $pkg $lived $nc"
  fi
  jq -r '.files[] | .file_name as $f | .mutations[] | select(.status == "LIVED") | "      LIVED \($f):\(.line):\(.column) \(.type)"' "$report"
done < test/mutation/baseline.txt
exit "$failed"
