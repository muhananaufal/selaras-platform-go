#!/usr/bin/env bash
# Point-in-time recovery, proven end to end against THROWAWAY containers -
# never the stack's database or its repository volume.
#
#   1. A primary built from deploy/compose/postgres archives its WAL to a
#      pgBackRest repository (the same image, config and flags as compose).
#   2. The backup sidecar script (deploy/compose/postgres/pitr.sh) creates
#      the stanza and takes the base backup.
#   3. Row A is written, the time T is taken from the database, row B is
#      written, and the WAL is switched so it reaches the archive.
#   4. Restored to T into a fresh container: A is there, B is not.
#      Restored to the end of the archive: both are there.
#
# Run: bash test/drill/pitr.test.sh   (needs Docker)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ID="pitr-test-$$"
IMAGE="$ID-image"
NET="$ID-net"
PRIMARY="$ID-primary"
REPO="$ID-repo"
DATA="$ID-data"
SOCKET="$ID-socket"

# shellcheck disable=SC2317 # invoked through the EXIT trap
cleanup() {
  docker rm -f "$PRIMARY" "$ID-restore-t" "$ID-restore-latest" >/dev/null 2>&1 || true
  docker volume rm -f "$REPO" "$DATA" "$SOCKET" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  docker image rm -f "$IMAGE" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker build -q -t "$IMAGE" "$ROOT/deploy/compose/postgres" >/dev/null
docker network create "$NET" >/dev/null
for v in "$REPO" "$DATA" "$SOCKET"; do docker volume create "$v" >/dev/null; done

# The primary, started with the image's own command - exactly what compose
# runs, archiving flags included.
docker run -d --name "$PRIMARY" --network "$NET" \
  -e POSTGRES_USER=admin -e POSTGRES_PASSWORD=throwaway -e POSTGRES_DB=selaras \
  -v "$DATA:/var/lib/postgresql" -v "$REPO:/var/lib/pgbackrest" -v "$SOCKET:/var/run/postgresql" \
  "$IMAGE" >/dev/null
for _ in $(seq 1 60); do
  docker exec "$PRIMARY" pg_isready -U admin -d selaras -h 127.0.0.1 >/dev/null 2>&1 && break
  sleep 1
done

psql() { docker exec -i "$PRIMARY" psql -U admin -d selaras -v ON_ERROR_STOP=1 -tA "$@"; }

# The sidecar, once: stanza and base backup, through the shared volumes.
docker run --rm --network "$NET" --user postgres -e PGBACKREST_PG1_USER=admin -e PITR_ONCE=1 \
  -v "$DATA:/var/lib/postgresql:ro" -v "$REPO:/var/lib/pgbackrest" -v "$SOCKET:/var/run/postgresql" \
  "$IMAGE" sh /usr/local/bin/pitr.sh

psql -c "CREATE TABLE drill (name text PRIMARY KEY)" >/dev/null
psql -c "INSERT INTO drill VALUES ('A')" >/dev/null
sleep 1
T=$(psql -c "SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US') || '+00'")
sleep 1
psql -c "INSERT INTO drill VALUES ('B')" >/dev/null
psql -c "SELECT pg_switch_wal()" >/dev/null
# check waits until the segment holding B is in the archive.
docker exec -e PGBACKREST_PG1_USER=admin "$PRIMARY" pgbackrest --stanza=selaras check >/dev/null

# restore <name> <extra pgbackrest options...> - restores into a fresh data
# directory in a new container, starts it, and prints the drill rows.
restore() {
  local name="$1"; shift
  docker run -d --name "$name" --network "$NET" -v "$REPO:/var/lib/pgbackrest" \
    --entrypoint sh "$IMAGE" -c "sleep infinity" >/dev/null
  docker exec -u postgres "$name" install -d -m 700 /tmp/restored
  docker exec -u postgres "$name" pgbackrest --stanza=selaras --pg1-path=/tmp/restored "$@" restore >/dev/null
  docker exec -u postgres "$name" pg_ctl -D /tmp/restored -o "-p 5433 -c archive_mode=off" -w -t 120 start >/dev/null
  # Promotion ends recovery; wait until the server accepts writes.
  for _ in $(seq 1 60); do
    [ "$(docker exec -u postgres "$name" psql -U admin -p 5433 -d selaras -tAc 'SELECT pg_is_in_recovery()')" = "f" ] && break
    sleep 1
  done
  docker exec -u postgres "$name" psql -U admin -p 5433 -d selaras -tAc "SELECT string_agg(name, ',' ORDER BY name) FROM drill"
}

failed=0
expect() { # expect <what> <got> <want>
  if [ "$2" = "$3" ]; then echo "ok    $1: $2"; else echo "FAIL  $1: got '$2', want '$3'"; failed=1; fi
}

got=$(restore "$ID-restore-t" --type=time "--target=$T" --target-action=promote)
expect "restored to T ($T)" "$got" "A"

got=$(restore "$ID-restore-latest")
expect "restored to the end of the archive" "$got" "A,B"

exit "$failed"
