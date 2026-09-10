#!/usr/bin/env bash
# Creates the k3d cluster from deploy/k3d/cluster.yaml (F9-04). Idempotent:
# an existing cluster is left alone.
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if k3d cluster list 2>/dev/null | grep -q '^selaras '; then
  echo "cluster selaras already exists"
else
  k3d cluster create --config "$ROOT/deploy/k3d/cluster.yaml"
fi
# k3d sees DOCKER_HOST tcp:// in WSL and writes kubernetes.docker.internal as
# the API address, while the port is published on 127.0.0.1:26443
# (cluster.yaml). The address is rewritten so kubectl works without guessing.
kubectl config set-cluster k3d-selaras --server=https://127.0.0.1:26443 >/dev/null
kubectl config use-context k3d-selaras >/dev/null
kubectl get nodes
