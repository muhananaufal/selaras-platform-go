#!/usr/bin/env bash
# Membuat klaster k3d dari deploy/k3d/cluster.yaml (F9-04). Idempoten:
# klaster yang sudah ada dibiarkan.
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if k3d cluster list 2>/dev/null | grep -q '^selaras '; then
  echo "cluster selaras already exists"
else
  k3d cluster create --config "$ROOT/deploy/k3d/cluster.yaml"
fi
# k3d melihat DOCKER_HOST tcp:// di WSL dan menulis kubernetes.docker.internal
# sebagai alamat API, padahal portnya dipublikasikan di 127.0.0.1:26443
# (cluster.yaml). Alamatnya ditulis ulang supaya kubectl bekerja tanpa
# menebak.
kubectl config set-cluster k3d-selaras --server=https://127.0.0.1:26443 >/dev/null
kubectl config use-context k3d-selaras >/dev/null
kubectl get nodes
