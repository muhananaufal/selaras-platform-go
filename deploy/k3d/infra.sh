#!/usr/bin/env bash
# Finishes the dependencies on the k3d cluster once OpenTofu has applied them
# (F9-04, F9-06, F9-20, F9-22): the Grafana admin Secret, waiting for the
# dependencies to be ready, and the migration Job per schema and the topics
# Job.
#
# Namespaces, the initdb and rule ConfigMaps, Postgres, Kafka, Redis,
# Mailpit, PgBouncer, KEDA and the observability stack are declared in
# deploy/iac/k3d and applied by deploy/k3d/iac.sh. What stays here is what
# does not belong in OpenTofu state: a Secret (state holds values in plain
# text) and run-to-completion Jobs.
#
# The order in `task k3d:all`: up -> iac -> secrets -> import -> infra ->
# deploy. The Jobs use the selaras/migrate and selaras/topics images with
# imagePullPolicy Never, so they only run after deploy/k3d/import.sh.

set -euo pipefail
# k3d, kubectl, and helm are installed in the WSL user's ~/.local/bin.
export PATH="$HOME/.local/bin:$PATH"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

log() { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*"; }

log "metrics-server (F9-20): bawaan k3s, tidak dipasang ulang"
# k3s ships its own metrics-server in kube-system, already trusted by the
# kubelet certificates. Installing the upstream release beside it produces
# two APIServices for metrics.k8s.io that overwrite each other. Its version
# follows k3s (rancher/k3s:v1.36.4-k3s1 in cluster.yaml).
kubectl -n kube-system get deployment metrics-server >/dev/null

log "Grafana admin Secret (dari .env, bukan dari manifest atau state)"
set -a; . "$ROOT/.env"; set +a
[ -n "${GRAFANA_ADMIN_PASSWORD:-}" ] || { echo "FATAL: GRAFANA_ADMIN_PASSWORD is not set in .env" >&2; exit 1; }
kubectl -n observability create secret generic grafana-admin \
  --from-literal=password="$GRAFANA_ADMIN_PASSWORD" --dry-run=client -o yaml | kubectl apply -f -

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
