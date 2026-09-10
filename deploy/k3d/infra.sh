#!/usr/bin/env bash
# Installs the dependencies, migrations, topics, and observability on the
# k3d cluster (F9-04, F9-06, F9-20, F9-22).
#
# The order: namespace -> initdb ConfigMap (from the SAME compose script) ->
# Postgres, Kafka, Redis, Mailpit, PgBouncer -> metrics-server -> KEDA ->
# observability -> wait for ready -> the migration Job per schema and the
# topics Job.
#
# metrics-server and KEDA are installed from the official release manifests
# with pinned versions; the pins are stated here so a change shows up in a
# diff, not in a "latest" that moves on its own.
#
# The migration and topics Jobs use the selaras/migrate and selaras/topics
# images - run deploy/k3d/import.sh first.

set -euo pipefail
# k3d, kubectl, and helm are installed in the WSL user's ~/.local/bin.
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
# The alert rules from the SAME file as compose; tested by promtool in CI.
kubectl -n observability create configmap prometheus-rules \
  --from-file=alerts.yml="$ROOT/deploy/compose/observability/alerts.yml" --dry-run=client -o yaml | kubectl apply -f -

log "dependensi"
kubectl apply -f "$ROOT/deploy/k8s/infra/"

log "metrics-server (F9-20): bawaan k3s, tidak dipasang ulang"
# k3s ships its own metrics-server in kube-system, already trusted by the
# kubelet certificates. Installing the upstream release beside it produces
# two APIServices for metrics.k8s.io that overwrite each other. Its version
# follows k3s (rancher/k3s:v1.36.4-k3s1 in cluster.yaml).
kubectl -n kube-system get deployment metrics-server >/dev/null

log "KEDA $KEDA_VERSION (F9-22)"
kubectl apply --server-side -f "https://github.com/kedacore/keda/releases/download/${KEDA_VERSION}/keda-${KEDA_VERSION#v}.yaml"

log "observability (F9-06)"
# The dashboards from the SAME files as compose; the Grafana admin password
# from .env, not from the manifest.
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
# Jobs cannot be updated in place; the old ones are deleted so re-applying
# after a new migration really runs them again.
kubectl -n selaras delete job -l selaras/job=migrate --ignore-not-found >/dev/null
kubectl -n selaras delete job topics --ignore-not-found >/dev/null
kubectl apply -f "$ROOT/deploy/k8s/jobs/migrate.yaml"
for s in identity profile assessment coaching chat nutrition dashboard llm; do
  kubectl -n selaras label job "migrate-$s" selaras/job=migrate --overwrite >/dev/null
  kubectl -n selaras wait --for=condition=complete "job/migrate-$s" --timeout=180s
done
kubectl -n selaras wait --for=condition=complete job/topics --timeout=180s

log "infra siap"
