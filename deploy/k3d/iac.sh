#!/usr/bin/env bash
# Applies what runs inside the k3d cluster as code (deploy/iac/k3d, OpenTofu):
# namespaces, configuration, the dependencies, KEDA and observability.
# docs/runbook/iac.md explains what is and is not managed there.
#
# Idempotent: a second run changes nothing unless the cluster drifted, in
# which case it puts the declared state back.
set -euo pipefail
# k3d, kubectl, helm and tofu are installed in the WSL user's ~/.local/bin.
export PATH="$HOME/.local/bin:$PATH"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIR="$ROOT/deploy/iac/k3d"

tofu -chdir="$DIR" init -input=false >/dev/null
tofu -chdir="$DIR" apply -input=false -auto-approve
