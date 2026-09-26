#!/usr/bin/env bash
# Proves that deploy/iac/k3d really owns what it declares, against a running
# k3d cluster (CI creates a fresh one; locally: task k3d:up first).
#
#   1. apply, then plan         -> exit 0: the cluster matches the code.
#   2. edit a ConfigMap by hand -> plan exit 2: drift is seen.
#   3. scale a Deployment and change a StatefulSet image by hand (objects
#      decoded from deploy/k8s YAML) -> plan exit 2.
#   4. apply                    -> the hand edits are undone, plan exit 0.
#
# Run: bash test/iac/drift.test.sh
set -uo pipefail

export PATH="$HOME/.local/bin:$PATH"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIR="$ROOT/deploy/iac/k3d"

failed=0
check() { # check <what> <got> <want>
  if [ "$2" = "$3" ]; then echo "ok    $1"; else echo "FAIL  $1: got $2, want $3"; failed=1; fi
}
plan() {
  tofu -chdir="$DIR" plan -input=false -no-color -detailed-exitcode >"$LOG" 2>&1
  echo $?
}
apply() {
  tofu -chdir="$DIR" apply -input=false -no-color -auto-approve >"$LOG" 2>&1
  echo $?
}
LOG="$(mktemp)"
trap 'rm -f "$LOG"' EXIT

tofu -chdir="$DIR" init -input=false -no-color >/dev/null || { echo "FAIL  tofu init"; exit 1; }

check "apply succeeds" "$(apply)" 0
check "plan right after apply shows no changes" "$(plan)" 0

kubectl -n observability patch configmap prometheus-rules --type merge \
  -p '{"data":{"alerts.yml":"groups: []\n"}}' >/dev/null
check "a hand-edited ConfigMap is seen as drift" "$(plan)" 2

check "apply puts it back" "$(apply)" 0
kubectl -n selaras scale deployment redis --replicas=3 >/dev/null
kubectl -n selaras patch statefulset postgres --type json \
  -p '[{"op":"replace","path":"/spec/template/spec/containers/0/image","value":"postgres:17-alpine"}]' >/dev/null
check "hand edits to objects from the YAML are seen as drift" "$(plan)" 2

check "apply reclaims the edited fields" "$(apply)" 0
check "plan after the second apply shows no changes" "$(plan)" 0
check "redis is back to the declared replicas" "$(kubectl -n selaras get deployment redis -o jsonpath='{.spec.replicas}')" 1
check "postgres is back to the declared image" \
  "$(kubectl -n selaras get statefulset postgres -o jsonpath='{.spec.template.spec.containers[0].image}')" "postgres:18.6-alpine"

exit "$failed"
