#!/usr/bin/env bash
# Memasang dependensi, migrasi, topic, dan observability di klaster k3d
# (F9-04, F9-06, F9-20, F9-22).
#
# Urutannya: namespace -> initdb ConfigMap (dari skrip compose yang SAMA) ->
# Postgres, Kafka, Redis, Mailpit, PgBouncer -> metrics-server -> KEDA ->
# observability -> tunggu siap -> Job migrasi per skema dan Job topic.
#
# metrics-server dan KEDA dipasang dari manifest rilis resmi dengan versi
# yang dipin; yang dipin dinyatakan di sini supaya perubahannya terlihat di
# diff, bukan di "latest" yang bergerak sendiri.
#
# Job migrasi dan topic memakai image selaras/migrate dan selaras/topics -
# jalankan deploy/k3d/import.sh lebih dulu.

set -euo pipefail
# k3d, kubectl, dan helm dipasang di ~/.local/bin milik pengguna WSL.
export PATH="$HOME/.local/bin:$PATH"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

KEDA_VERSION="v2.20.2"

log() { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*"; }

kubectl get namespace selaras >/dev/null 2>&1 || kubectl create namespace selaras
kubectl get namespace observability >/dev/null 2>&1 || kubectl create namespace observability

log "initdb ConfigMap dari deploy/compose/initdb/01-schemas.sh (satu sumber untuk isolasi skema)"
kubectl -n selaras create configmap postgres-initdb \
  --from-file=01-schemas.sh="$ROOT/deploy/compose/initdb/01-schemas.sh" \
  --dry-run=client -o yaml | kubectl apply -f -
# Aturan alert dari berkas yang SAMA dengan compose; diuji promtool di CI.
kubectl -n observability create configmap prometheus-rules \
  --from-file=alerts.yml="$ROOT/deploy/compose/observability/alerts.yml" --dry-run=client -o yaml | kubectl apply -f -

log "dependensi"
kubectl apply -f "$ROOT/deploy/k8s/infra/"

log "metrics-server (F9-20): bawaan k3s, tidak dipasang ulang"
# k3s membawa metrics-server sendiri di kube-system, sudah dipercaya
# sertifikat kubelet-nya. Memasang rilis upstream di sampingnya menghasilkan
# dua APIService untuk metrics.k8s.io yang saling menimpa. Versinya
# mengikuti k3s (rancher/k3s:v1.36.4-k3s1 di cluster.yaml).
kubectl -n kube-system get deployment metrics-server >/dev/null

log "KEDA $KEDA_VERSION (F9-22)"
kubectl apply --server-side -f "https://github.com/kedacore/keda/releases/download/${KEDA_VERSION}/keda-${KEDA_VERSION#v}.yaml"

log "observability (F9-06)"
# Dashboard dari berkas yang SAMA dengan compose; kata sandi admin Grafana
# dari .env, bukan dari manifest.
set -a; . "$ROOT/.env"; set +a
[ -n "${GRAFANA_ADMIN_PASSWORD:-}" ] || { echo "FATAL: GRAFANA_ADMIN_PASSWORD is not set in .env" >&2; exit 1; }
kubectl -n observability create configmap grafana-dashboards \
  --from-file="$ROOT/deploy/grafana/dashboards/" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n observability create secret generic grafana-admin \
  --from-literal=password="$GRAFANA_ADMIN_PASSWORD" --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f "$ROOT/deploy/k8s/observability/"

log "menunggu Postgres, Kafka, PgBouncer, metrics-server, KEDA"
kubectl -n selaras rollout status statefulset/postgres --timeout=180s
kubectl -n selaras rollout status statefulset/kafka --timeout=240s
kubectl -n selaras rollout status deployment/pgbouncer --timeout=120s
kubectl -n kube-system rollout status deployment/metrics-server --timeout=180s
kubectl -n keda rollout status deployment/keda-operator --timeout=180s

log "migrasi per skema dan topic Kafka (Job)"
# Job tidak bisa diperbarui di tempat; yang lama dihapus supaya apply ulang
# setelah ada migrasi baru benar-benar menjalankannya lagi.
kubectl -n selaras delete job -l selaras/job=migrate --ignore-not-found >/dev/null
kubectl -n selaras delete job topics --ignore-not-found >/dev/null
kubectl apply -f "$ROOT/deploy/k8s/jobs/migrate.yaml"
for s in identity profile assessment coaching chat nutrition dashboard llm; do
  kubectl -n selaras label job "migrate-$s" selaras/job=migrate --overwrite >/dev/null
  kubectl -n selaras wait --for=condition=complete "job/migrate-$s" --timeout=180s
done
kubectl -n selaras wait --for=condition=complete job/topics --timeout=180s

log "infra siap"
