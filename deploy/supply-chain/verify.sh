#!/usr/bin/env bash
# Verifies that an artifact was built by THIS repository's workflow on a
# GitHub-hosted runner, with a signed SLSA provenance and a signed CycloneDX
# SBOM attached (both made by actions/attest, keyless through Sigstore).
#
# Usage: verify.sh <subject> <signer-workflow-path>
#   subject          a file path, or oci://ghcr.io/<owner>/selaras/<unit>:<tag>
#   signer-workflow  e.g. .github/workflows/cd.yml - an attestation signed by
#                    any other workflow of the repository is refused
# Needs GH_TOKEN and GITHUB_REPOSITORY (both set on Actions runners).
set -euo pipefail
subject="${1:?usage: verify.sh <subject> <signer-workflow-path>}"
workflow="${2:?usage: verify.sh <subject> <signer-workflow-path>}"
repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}"

for predicate in https://slsa.dev/provenance/v1 https://cyclonedx.org/bom; do
  gh attestation verify "$subject" \
    --repo "$repo" \
    --signer-workflow "$repo/$workflow" \
    --predicate-type "$predicate" \
    --deny-self-hosted-runners >/dev/null
  echo "verified $predicate for $subject"
done
