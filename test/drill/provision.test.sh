#!/usr/bin/env bash
# Tests deploy/compose/initdb/01-schemas.sh against a THROWAWAY Postgres.
#
# The script runs as initdb on a fresh volume, and it must also be safe to
# run again on a database that already has everything (task db:provision):
# that is the only way a unit added later - or a rotated password - reaches a
# cluster whose initdb ran long ago.
#
# Run: bash test/drill/provision.test.sh   (needs Docker)
# run, as_role and refused are invoked through check, which shellcheck cannot follow.
# shellcheck disable=SC2329
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IMAGE=postgres:18.6-alpine
NAME="provision-test-$$"
UNITS="IDENTITY PROFILE ASSESSMENT COACHING CHAT NUTRITION DASHBOARD LLM"
# shellcheck disable=SC2317 # invoked through the EXIT trap
cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

envs=()
for u in $UNITS; do envs+=(-e "SVC_${u}_PASSWORD=first-${u}"); done
docker run -d --name "$NAME" -e POSTGRES_PASSWORD=throwaway -e POSTGRES_DB=selaras "${envs[@]}" "$IMAGE" >/dev/null
for _ in $(seq 1 60); do
  # Over TCP: the entrypoint's temporary init server listens on the socket
  # only, and a socket check can catch it just before it shuts down.
  docker exec "$NAME" pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1 && break
  sleep 1
done
docker cp "$ROOT/deploy/compose/initdb/01-schemas.sh" "$NAME:/tmp/01-schemas.sh" >/dev/null

# run <extra env...>: the script as initdb runs it - psql as the superuser on
# the socket, with the unit passwords in the environment.
run() {
  docker exec -e POSTGRES_USER=postgres -e POSTGRES_DB=selaras "$@" "$NAME" bash /tmp/01-schemas.sh
}
# Over the container's own address, NOT 127.0.0.1: the image trusts loopback
# connections without a password, and a password check over loopback passes
# with any password at all.
ADDR="$(docker exec "$NAME" hostname -i | awk '{print $1}')"
as_role() { # as_role <role> <password> <sql>
  docker exec -e PGPASSWORD="$2" "$NAME" psql -h "$ADDR" -U "$1" -d selaras -v ON_ERROR_STOP=1 -qtAc "$3"
}

failed=0
check() { # check <name> <command...>
  local name="$1"; shift
  if "$@" >/tmp/provision-out-$$ 2>&1; then echo "ok    $name"; else
    echo "FAIL  $name"; sed 's/^/      /' /tmp/provision-out-$$; failed=1
  fi
}
refused() { ! "$@"; }

check "a fresh database is provisioned" run
check "running it again on a provisioned database succeeds" run
check "a unit role works in its own schema" \
  as_role svc_chat first-CHAT "CREATE TABLE chat.provision_probe (id int); DROP TABLE chat.provision_probe;"
check "a unit role is refused its neighbour's schema" \
  refused as_role svc_chat first-CHAT "CREATE TABLE identity.provision_probe (id int);"

# The control for the password checks below: they would prove nothing over a
# connection that does not ask for a password.
check "a wrong password is refused" refused as_role svc_chat not-the-password "SELECT 1"

# A rotated password in the environment reaches the role on the next run.
check "a rotated password is applied on the next run" run -e SVC_CHAT_PASSWORD=rotated-CHAT
check "the role logs in with the rotated password" as_role svc_chat rotated-CHAT "SELECT 1"
check "the old password no longer works" refused as_role svc_chat first-CHAT "SELECT 1"

# A missing password is still refused, on a rerun too.
check "a missing password is refused" refused run -e SVC_LLM_PASSWORD=

rm -f /tmp/provision-out-$$
exit "$failed"
