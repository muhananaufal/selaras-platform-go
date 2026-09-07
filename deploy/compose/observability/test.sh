#!/bin/bash
# Uji aturan alert dan konfigurasi Alertmanager dengan image yang sama
# dengan yang dijalankan compose dan k8s. Dipanggil `task alerts:test`
# (dari WSL, karena bind mount) dan job `alert rules` di CI.
set -euo pipefail
cd "$(dirname "$0")"
docker run --rm -v "$PWD:/rules:ro" --entrypoint promtool prom/prometheus:v3.14.0 check rules /rules/alerts.yml
docker run --rm -v "$PWD:/rules:ro" -w /rules --entrypoint promtool prom/prometheus:v3.14.0 test rules alerts_test.yml
docker run --rm -v "$PWD:/am:ro" --entrypoint amtool prom/alertmanager:v0.34.0 check-config /am/alertmanager.yml
