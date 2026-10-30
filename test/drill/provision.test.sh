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
envs+=(-e CLINIC_OWNER_PASSWORD=first-OWNER -e SVC_CLINIC_PASSWORD=first-CLINIC -e OPENFGA_DB_PASSWORD=first-OPENFGA)
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
# The stored SCRAM verifier, read as the superuser on the socket.
verifier() { docker exec "$NAME" psql -U postgres -d selaras -qtAc "SELECT rolpassword FROM pg_authid WHERE rolname = '$1'"; }
before=$(verifier svc_chat)
check "running it again on a provisioned database succeeds" run
# A rerun must not re-hash an unchanged password: a new SCRAM salt breaks
# PgBouncer, which still holds the old secret, and every unit behind it
# fails to log in until PgBouncer restarts (seen on the local stack).
check "a rerun leaves the password verifier as it was" test "$(verifier svc_chat)" = "$before"
check "a unit role works in its own schema" \
  as_role svc_chat first-CHAT "CREATE TABLE chat.provision_probe (id int); DROP TABLE chat.provision_probe;"
check "a unit role is refused its neighbour's schema" \
  refused as_role svc_chat first-CHAT "CREATE TABLE identity.provision_probe (id int);"

# The control for the password checks below: they would prove nothing over a
# connection that does not ask for a password.
check "a wrong password is refused" refused as_role svc_chat not-the-password "SELECT 1"

# A changed password in the environment is applied only when a rotation is
# asked for - and then it is.
check "a changed password is not applied without a rotation" run -e SVC_CHAT_PASSWORD=rotated-CHAT
check "the role still logs in with its old password" as_role svc_chat first-CHAT "SELECT 1"
check "a rotation applies the changed password" run -e SVC_CHAT_PASSWORD=rotated-CHAT -e ROTATE_PASSWORDS=yes
check "the role logs in with the rotated password" as_role svc_chat rotated-CHAT "SELECT 1"
check "the old password no longer works" refused as_role svc_chat first-CHAT "SELECT 1"

# ADR-030: the clinic schema belongs to its migration role, and the runtime
# role can create nothing in it - a role that owns a table can switch its
# triggers and row level security off.
owner_of_clinic() {
  as_role clinic_owner first-OWNER "SELECT nspowner::regrole::text FROM pg_namespace WHERE nspname = 'clinic'"
}
check "clinic_owner owns the clinic schema" test "$(owner_of_clinic)" = clinic_owner
check "clinic_owner can create in the clinic schema" \
  as_role clinic_owner first-OWNER "CREATE TABLE clinic.provision_probe (id int); DROP TABLE clinic.provision_probe;"
check "svc_clinic cannot create in the clinic schema" \
  refused as_role svc_clinic first-CLINIC "CREATE TABLE clinic.provision_probe (id int);"
check "svc_clinic is refused another unit's schema" \
  refused as_role svc_clinic first-CLINIC "CREATE TABLE chat.provision_probe (id int);"
check "a missing clinic password is refused" refused run -e SVC_CLINIC_PASSWORD=

# OpenFGA keeps its tuples in its own database, owned by its own role
# (ADR-030): its migrations create their own tables, and nothing of theirs
# belongs in a unit's schema.
check "svc_openfga works in the openfga database" \
  docker exec -e PGPASSWORD=first-OPENFGA "$NAME" psql -h "$ADDR" -U svc_openfga -d openfga -v ON_ERROR_STOP=1 -qc \
  "CREATE TABLE provision_probe (id int); DROP TABLE provision_probe;"
check "svc_openfga cannot reach the application database" \
  refused as_role svc_openfga first-OPENFGA "SELECT 1"
check "a unit role cannot reach the openfga database" \
  refused docker exec -e PGPASSWORD=first-CHAT "$NAME" psql -h "$ADDR" -U svc_chat -d openfga -v ON_ERROR_STOP=1 -qc "SELECT 1"
check "a missing openfga password is refused" refused run -e OPENFGA_DB_PASSWORD=

# A missing password is still refused, on a rerun too.
check "a missing password is refused" refused run -e SVC_LLM_PASSWORD=

rm -f /tmp/provision-out-$$
exit "$failed"
