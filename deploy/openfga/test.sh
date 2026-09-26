#!/usr/bin/env bash
# Tests the clinic access model (ADR-030) with the official fga CLI.
#
# First proves the gate can fail: must-fail.fga.yaml holds a wrong
# expectation, and `fga model test` has to fail on it BECAUSE OF THE
# ASSERTION. Any failure is not enough - the first version of this fixture
# failed only because fga refuses a model_file outside the test file's
# directory, which would have "proven" nothing.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
FGA="${FGA:-fga}"

if out=$("$FGA" model test --tests must-fail.fga.yaml 2>&1); then
  echo "the must-fail fixture passed: fga model test does not fail on a wrong assertion" >&2
  exit 1
fi
if ! grep -q "Tests 0/1 passing" <<<"$out"; then
  printf 'the must-fail fixture failed for another reason:\n%s\n' "$out" >&2
  exit 1
fi
echo "gate proven: a wrong assertion fails the test"

"$FGA" model validate --file model.fga
"$FGA" model test --tests model.fga.yaml
