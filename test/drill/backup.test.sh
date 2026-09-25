#!/usr/bin/env bash
# Tests deploy/compose/backup/backup.sh against a THROWAWAY Postgres and a
# throwaway backup directory - never the stack's database or its volume.
#
# What is pinned: a round that fails leaves NOTHING under a final name (the
# volume held seven empty dumps and five empty role files that looked like
# backups), and a round only succeeds when the archive lists the eight
# schemas AND the role file carries the eight svc_* roles.
#
# Run: bash test/drill/backup.test.sh   (needs Docker)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IMAGE=postgres:18.6-alpine
PG="backup-test-pg-$$"
UNITS="identity profile assessment coaching chat nutrition dashboard llm"
# shellcheck disable=SC2317 # invoked through the EXIT trap
cleanup() { docker rm -f "$PG" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run -d --name "$PG" -e POSTGRES_USER=admin -e POSTGRES_PASSWORD=throwaway -e POSTGRES_DB=selaras "$IMAGE" >/dev/null
for _ in $(seq 1 60); do
  docker exec "$PG" pg_isready -U admin -d selaras -h 127.0.0.1 >/dev/null 2>&1 && break
  sleep 1
done
for u in $UNITS; do
  docker exec "$PG" psql -U admin -d selaras -v ON_ERROR_STOP=1 -qc \
    "CREATE ROLE svc_$u LOGIN PASSWORD 'x'; CREATE SCHEMA $u AUTHORIZATION svc_$u; CREATE TABLE $u.t (id int);"
done

# backup <host> <once:0|1> <seconds> - runs backup.sh in a throwaway
# container that shares the database container's network, then lists the
# backup directory. Prints "exit=<code>" and one "file <name> <bytes>" per
# file with a final name.
backup() {
  docker run --rm --network "container:$PG" \
    -v "$ROOT/deploy/compose/backup/backup.sh:/backup.sh:ro" \
    -e PGHOST="$1" -e PGUSER=admin -e PGPASSWORD=throwaway -e PGDATABASE=selaras \
    -e PGCONNECT_TIMEOUT=2 -e BACKUP_DIR=/tmp/b -e BACKUP_INTERVAL=3600 -e BACKUP_ONCE="$2" \
    --entrypoint sh "$IMAGE" -c "
      mkdir -p /tmp/b
      timeout $3 sh /backup.sh >/tmp/log 2>&1; echo exit=\$?
      for f in /tmp/b/*; do [ -e \"\$f\" ] || continue; case \"\$f\" in *.tmp) continue;; esac
        echo \"file \$(basename \"\$f\") \$(wc -c < \"\$f\")\"; done
      sed 's/^/log /' /tmp/log"
}

failed=0
check() { # check <name> <condition-output-grep> <must-match:yes|no>
  if printf '%s\n' "$out" | grep -qE "$2"; then hit=yes; else hit=no; fi
  if [ "$hit" = "$3" ]; then echo "ok    $1"; else echo "FAIL  $1"; printf '%s\n' "$out" | sed 's/^/      /'; failed=1; fi
}

# 1. The database is unreachable while the service loop runs: no file may
#    appear under a final name.
out=$(backup no-such-host 0 8)
check "an unreachable database leaves no file under a final name" '^file ' no

# 2. A healthy database: both files, non-empty, verified.
out=$(backup 127.0.0.1 1 60)
check "a healthy round exits 0" '^exit=0$' yes
check "a healthy round writes a non-empty role file" '^file globals-[0-9TZ]+\.sql [1-9][0-9]*$' yes
check "a healthy round writes a non-empty dump" '^file selaras-[0-9TZ]+\.dump [1-9][0-9]*$' yes
check "a healthy round logs its verification" 'log .*verified .*schemas=8 .*roles=8' yes

# 3. One unit role is gone: the role file is not a complete set, so the
#    round fails and leaves nothing behind.
docker exec "$PG" psql -U admin -d selaras -v ON_ERROR_STOP=1 -qc \
  "ALTER SCHEMA llm OWNER TO admin; ALTER TABLE llm.t OWNER TO admin; DROP ROLE svc_llm;"
out=$(backup 127.0.0.1 1 60)
check "a missing unit role fails the round" '^exit=0$' no
check "a failed round leaves no file under a final name" '^file ' no

exit "$failed"
