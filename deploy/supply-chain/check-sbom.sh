#!/usr/bin/env bash
# Checks that an SBOM describes the binary it claims to describe.
#
# Usage: check-sbom.sh <cyclonedx.json> <module>...
#
# Each <module> must appear as a component at EXACTLY the version the module
# graph resolves (`go list -m`). The SBOM is read from the binary's embedded
# build info and the expectation from go.mod, so the two are independent: an
# SBOM of the wrong binary, a stale one, or an empty one fails here.
set -euo pipefail
sbom="${1:?usage: check-sbom.sh <cyclonedx.json> <module>...}"
shift
[ "$#" -gt 0 ] || { echo "name at least one module to look for" >&2; exit 2; }

format=$(jq -r '.bomFormat // empty' "$sbom")
[ "$format" = "CycloneDX" ] || { echo "not a CycloneDX document: bomFormat='$format'" >&2; exit 1; }

total=$(jq '[.components[]? | select(.purl // "" | startswith("pkg:golang/"))] | length' "$sbom")
echo "Go components in the SBOM: $total"

failed=0
for mod in "$@"; do
  want=$(go list -m -f '{{.Version}}' "$mod")
  got=$(jq -r --arg m "$mod" '[.components[]? | select(.name == $m) | .version] | first // empty' "$sbom")
  if [ "$got" = "$want" ]; then
    echo "ok       $mod $got"
  else
    echo "MISMATCH $mod: SBOM '${got:-absent}', go.mod '$want'" >&2
    failed=1
  fi
done
exit "$failed"
