#!/bin/bash
# Lint the chart for both value profiles, render, then validate against the
# Kubernetes schemas. Called by `task helm:lint` (from WSL) and the `helm
# chart` job in CI.
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
cd "$(dirname "$0")/../.."
helm lint --strict deploy/helm/selaras -f deploy/helm/selaras/values-local.yaml
helm lint --strict deploy/helm/selaras -f deploy/helm/selaras/values-cloud.yaml
helm template selaras deploy/helm/selaras -n selaras -f deploy/helm/selaras/values-cloud.yaml > /tmp/selaras-rendered.yaml
docker run --rm -i ghcr.io/yannh/kubeconform:v0.8.0 -strict -summary -ignore-missing-schemas -kubernetes-version 1.33.0 < /tmp/selaras-rendered.yaml
