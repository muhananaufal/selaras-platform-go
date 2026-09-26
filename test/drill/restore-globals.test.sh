#!/usr/bin/env bash
# Tests deploy/compose/backup/restore-globals.sh against a THROWAWAY Postgres
# (never the stack's database): the only error a roles restore may ignore is
# "role ... already exists"; every other error, and a missing file, must fail.
#
# Run: bash test/drill/restore-globals.test.sh   (needs Docker)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IMAGE=postgres:18.6-alpine
NAME="restore-globals-test-$$"
WORK="$(mktemp -d)"
# shellcheck disable=SC2317 # invoked through the EXIT trap
cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" -e POSTGRES_PASSWORD=throwaway "$IMAGE" >/dev/null
for _ in $(seq 1 60); do
  # Over TCP, not the socket: the image's entrypoint first runs a temporary
  # server on the socket only for initialisation and then restarts, and a
  # socket check can catch that one just before it shuts down.
  docker exec "$NAME" pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1 && break
  sleep 1
done
docker exec "$NAME" psql -U postgres -v ON_ERROR_STOP=1 -qc "CREATE ROLE svc_existing LOGIN PASSWORD 'old'"

# The script runs inside the same container, as the drill runs it inside a
# postgres image: psql is there, and nothing is installed on the host.
run() { docker exec -e PGUSER=postgres -e PGPASSWORD=throwaway "$NAME" sh /tmp/restore-globals.sh "$@"; }
docker cp "$ROOT/deploy/compose/backup/restore-globals.sh" "$NAME:/tmp/restore-globals.sh" >/dev/null

failed=0
expect() { # expect <want-exit> <name> <args...>
  local want="$1" name="$2"; shift 2
  local got=0
  run "$@" >"$WORK/out" 2>&1 || got=$?
  if { [ "$want" = 0 ] && [ "$got" = 0 ]; } || { [ "$want" != 0 ] && [ "$got" != 0 ]; }; then
    echo "ok    $name (exit $got)"
  else
    echo "FAIL  $name: exit $got, want $([ "$want" = 0 ] && echo 0 || echo non-zero)"; sed 's/^/      /' "$WORK/out"; failed=1
  fi
}

# 1. A globals file like pg_dumpall writes: an existing role, a new one, and
#    the ALTER ROLE that carries the password. Passes, and the new role and
#    the new password are really there.
cat >"$WORK/good.sql" <<'SQL'
CREATE ROLE svc_existing;
ALTER ROLE svc_existing WITH LOGIN PASSWORD 'restored';
CREATE ROLE svc_new;
ALTER ROLE svc_new WITH LOGIN PASSWORD 'restored';
SQL
docker cp "$WORK/good.sql" "$NAME:/tmp/good.sql" >/dev/null
expect 0 "existing roles are tolerated, the rest is applied" /tmp/good.sql
if [ "$(docker exec "$NAME" psql -U postgres -tAc "SELECT count(*) FROM pg_roles WHERE rolname IN ('svc_existing','svc_new') AND rolcanlogin")" != 2 ]; then
  echo "FAIL  the new role or its ALTER ROLE (LOGIN) was not applied"; failed=1
fi

# 2. Any other error fails the restore, even with the benign one next to it.
cat >"$WORK/bad.sql" <<'SQL'
CREATE ROLE svc_existing;
ALTER ROLE svc_missing WITH LOGIN PASSWORD 'x';
SQL
docker cp "$WORK/bad.sql" "$NAME:/tmp/bad.sql" >/dev/null
expect 1 "an error other than 'already exists' fails" /tmp/bad.sql

# 3. A file that is not there fails - the old `|| true` restored no roles at
#    all and carried on.
expect 1 "a missing globals file fails" /tmp/nothing-here.sql

# 4. An EMPTY file fails. The backup volume held five of them, and the
#    first version of this script restored "0 roles" from one and exited 0.
: >"$WORK/empty.sql"
docker cp "$WORK/empty.sql" "$NAME:/tmp/empty.sql" >/dev/null
expect 1 "an empty globals file fails" /tmp/empty.sql

# 5. No argument is a usage error.
expect 1 "no argument fails"

exit "$failed"
