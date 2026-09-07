#!/bin/bash
# Lint chart untuk kedua profil nilai, render, lalu validasi terhadap skema
# Kubernetes. Dipanggil `task helm:lint` (dari WSL) dan job `helm chart` di CI.
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
cd "$(dirname "$0")/../.."
helm lint --strict deploy/helm/selaras -f deploy/helm/selaras/values-local.yaml
helm lint --strict deploy/helm/selaras -f deploy/helm/selaras/values-cloud.yaml
helm template selaras deploy/helm/selaras -n selaras -f deploy/helm/selaras/values-cloud.yaml > /tmp/selaras-rendered.yaml
docker run --rm -i ghcr.io/yannh/kubeconform:v0.8.0 -strict -summary -ignore-missing-schemas -kubernetes-version 1.33.0 < /tmp/selaras-rendered.yaml
