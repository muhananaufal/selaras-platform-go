#!/bin/bash
# Tests the alert rules and Alertmanager configuration with the same images
# compose and k8s run. Called by `task alerts:test` (from WSL, because of
# the bind mount) and the `alert rules` job in CI.
set -euo pipefail
cd "$(dirname "$0")"
docker run --rm -v "$PWD:/rules:ro" --entrypoint promtool prom/prometheus:v3.14.0 check rules /rules/alerts.yml
docker run --rm -v "$PWD:/rules:ro" -w /rules --entrypoint promtool prom/prometheus:v3.14.0 test rules alerts_test.yml
docker run --rm -v "$PWD:/am:ro" --entrypoint amtool prom/alertmanager:v0.34.0 check-config /am/alertmanager.yml
