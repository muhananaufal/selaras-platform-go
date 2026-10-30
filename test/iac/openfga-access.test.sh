#!/usr/bin/env bash
# Proves the two walls around OpenFGA on a running k3d cluster (ADR-031),
# after task k3d:all:
#
#   1. a pod that is not one of the three allowed units cannot even connect
#      to port 8080, key or not (NetworkPolicy openfga-ingress);
#   2. a pod labelled as an allowed unit connects, and is still refused
#      without the key (401) and with a wrong one, and answered with it (200).
#
# The key is read inside the probe pod from the selaras-infra Secret; it is
# never printed or passed on a command line.
#
# Run: bash test/iac/openfga-access.test.sh
set -uo pipefail

export PATH="$HOME/.local/bin:$PATH"
NS=selaras
IMAGE=curlimages/curl:8.11.1

failed=0
check() { # check <what> <got> <want>
  if [ "$2" = "$3" ]; then echo "ok    $1"; else echo "FAIL  $1: got $2, want $3"; failed=1; fi
}

# probe <pod label> <auth: none|wrong|key> -> the HTTP status of GET /stores,
# or 000 when no connection succeeds within about ten seconds. The retries
# are for the policy controller: a pod that has just started is not in the
# allowed set yet, and its first connection is refused even when its label
# is allowed (seen here: "Failed to connect after 1 ms" from a clinic-svc
# label while the real clinic-svc was connected).
probe() {
  # --oauth2-bearer sends "Authorization: Bearer <token>". $KEY is expanded by
  # the shell inside the pod, not here; the key is hex, so it needs no quotes.
  local name="probe-$RANDOM" header
  case "$2" in
    none) header='' ;;
    wrong) header='--oauth2-bearer not-the-key' ;;
    key) header='--oauth2-bearer $KEY' ;;
  esac
  kubectl -n "$NS" run "$name" --rm -i --quiet --restart=Never --image="$IMAGE" \
    --labels="app.kubernetes.io/name=$1" \
    --overrides='{"spec":{"containers":[{"name":"'"$name"'","image":"'"$IMAGE"'",
      "env":[{"name":"KEY","valueFrom":{"secretKeyRef":{"name":"selaras-infra","key":"OPENFGA_API_TOKEN"}}}],
      "command":["sh","-c","for i in 1 2 3 4 5 6 7 8 9 10; do c=$(curl -s -o /dev/null -w %{http_code} --max-time 3 '"$header"' http://openfga:8080/stores); [ $c != 000 ] && break; sleep 1; done; echo $c"]}]}}' \
    2>/dev/null | tr -d '\r\n' | tail -c 3
}

kubectl -n "$NS" rollout status deployment/openfga --timeout=120s >/dev/null ||
  { echo "FAIL  openfga is not ready"; exit 1; }

check "a pod outside the three units cannot connect, even with the key" "$(probe intruder key)" 000
check "an allowed unit without the key is refused" "$(probe clinic-svc none)" 401
check "an allowed unit with a wrong key is refused" "$(probe coaching-svc wrong)" 401
check "an allowed unit with the key is answered" "$(probe assessment-svc key)" 200

exit "$failed"
