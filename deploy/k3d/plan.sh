#!/usr/bin/env bash
# Shows what OpenTofu would change in the cluster: the drift check.
# Exit 0 = the cluster matches deploy/iac/k3d, 2 = it has drifted, 1 = error.
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIR="$ROOT/deploy/iac/k3d"

tofu -chdir="$DIR" init -input=false >/dev/null
tofu -chdir="$DIR" plan -input=false -detailed-exitcode
