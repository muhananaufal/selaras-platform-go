#!/bin/bash
# Lints every migration newer than the frozen baseline in .squawk.toml with
# squawk, the same pinned image locally (task migrate:lint, through WSL) and
# in CI (the `migrations` job).
#
# Before linting, it proves the gate itself: a known-safe fixture must pass
# and a known-unsafe one must be REFUSED. A linter that silently accepts
# everything - a wrong config, an excluded path that matches too much - is
# worse than none, because it is trusted.
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE=ghcr.io/sbdchd/squawk:2.66.0
squawk() { docker run --rm -v "$PWD:/data:ro" "$IMAGE" --config .squawk.toml "$@"; }

squawk test/migrations/testdata/safe.up.sql
if squawk test/migrations/testdata/unsafe.up.sql >/dev/null 2>&1; then
  echo "squawk accepted test/migrations/testdata/unsafe.up.sql; the gate proves nothing" >&2
  exit 1
fi
# The drill's expand/contract migrations are the worked example the
# runbook points to; they must stay clean too.
squawk test/drill/expand/migrations/*.sql
echo "gate proven: the safe fixture passes, the unsafe one is refused"

# squawk exits non-zero on "no files", so the files newer than the baseline
# are chosen here, and none is a pass.
new=()
while IFS= read -r f; do
  grep -qF "\"$f\"" .squawk.toml || new+=("$f")
done < <(find migrations -name '*.up.sql' | sort)

if [ ${#new[@]} -eq 0 ]; then
  echo "no migration newer than the frozen baseline"
  exit 0
fi
echo "linting ${#new[@]} migration(s): ${new[*]}"
squawk "${new[@]}"
