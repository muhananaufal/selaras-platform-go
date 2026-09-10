#!/usr/bin/env bash
# Installs or upgrades the selaras chart with the local values (F9-04).
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
helm upgrade --install selaras "$ROOT/deploy/helm/selaras" -n selaras \
  -f "$ROOT/deploy/helm/selaras/values-local.yaml" --wait --timeout 5m "$@"
kubectl -n selaras get pods
