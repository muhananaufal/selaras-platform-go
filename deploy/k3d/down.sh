#!/usr/bin/env bash
# Deletes the k3d cluster with everything in it, and the local OpenTofu state
# that described it: the state lives and dies with this cluster, and a state
# that outlives it only describes objects that no longer exist.
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

k3d cluster delete selaras
rm -f "$ROOT"/deploy/iac/k3d/terraform.tfstate "$ROOT"/deploy/iac/k3d/terraform.tfstate.backup
