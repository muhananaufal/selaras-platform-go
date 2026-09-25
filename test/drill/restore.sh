#!/usr/bin/env bash
# Recovery drill from zero (F9-31, ADR-016, exit criterion #14).
#
# The database is DROPPED ENTIRELY and then restored from the latest backup
# in the backups volume. Every step is timed, and this script does NOT run
# the e2e tests itself - it prints the command at the end, to be run from
# Windows (the Go toolchain lives there), and the result is recorded in
# docs/runbook/restore-drill.md together with the timings.
#
# Run from WSL: bash test/drill/restore.sh
#
# This is DESTRUCTIVE: all local data is replaced with the backup's content.
# That is the point. It refuses to run without DRILL=yes.

set -euo pipefail

[ "${DRILL:-}" = "yes" ] || { echo "refusing: this drops the database. Run with DRILL=yes."; exit 2; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
set -a; . "$ROOT/.env"; set +a
COMPOSE="docker compose --env-file $ROOT/.env -f $ROOT/deploy/compose/core.yml -f $ROOT/deploy/compose/apps.yml -f $ROOT/deploy/compose/full.yml -f $ROOT/deploy/compose/backup.yml"
UNITS="identity-svc profile-svc assessment-svc coaching-svc chat-svc nutrition-svc dashboard-svc llm-worker edge-gateway pgbouncer backup"

log() { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*"; }
# psql_admin speaks to the "postgres" database (for DROP/CREATE), psql_app to
# the application database (for counting its content).
psql_admin() { docker exec -i selaras-postgres psql -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 -tA -c "$1"; }
psql_app() { docker exec -i selaras-postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -tA -c "$1"; }
started=$(date +%s)
mark() { echo $(( $(date +%s) - started )); }

# 1. The backup is picked BEFORE anything is deleted: the newest NON-EMPTY
#    archive, and the role file of the SAME round (same stamp). Picking the
#    newest of each separately could pair files from different rounds, and
#    the volume has held empty files under final names (see backup.sh).
PAIR=$(docker run --rm -v selaras-core_backups:/backups alpine sh -c '
  for dump in $(ls -1 /backups/*.dump 2>/dev/null | sort -r); do
    [ -s "$dump" ] || continue
    stamp=${dump##*-}; stamp=${stamp%.dump}
    globals=/backups/globals-$stamp.sql
    [ -s "$globals" ] || continue
    echo "$dump $globals"; break
  done')
LATEST=${PAIR% *}
GLOBALS=${PAIR#* }
[ -n "$PAIR" ] || { log "no non-empty archive with a matching role file in the backups volume"; exit 1; }
log "restoring from $LATEST (+ $GLOBALS)"

# The numbers before: used to compare after the restore.
BEFORE=$(psql_app "SELECT (SELECT count(*) FROM identity.users) || ' users, ' || (SELECT count(*) FROM assessment.risk_assessments) || ' assessments'" 2>/dev/null || echo "unknown")
log "before: $BEFORE"

# 2. Everything holding a connection is stopped.
log "stopping units and pgbouncer"
# shellcheck disable=SC2086
$COMPOSE stop $UNITS >/dev/null 2>&1
log "stopped at +$(mark)s"

# 3. The database is dropped ENTIRELY.
psql_admin "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$POSTGRES_DB' AND pid <> pg_backend_pid()" >/dev/null
psql_admin "DROP DATABASE $POSTGRES_DB"
log "database dropped at +$(mark)s"
psql_admin "CREATE DATABASE $POSTGRES_DB"

# 4. The roles, then the content. The roles usually exist already, so their
#    CREATE ROLE fails with "already exists" - restore-globals.sh ignores that
#    one error and fails on any other, and on a missing globals file
#    (tested in test/drill/restore-globals.test.sh).
docker run --rm -v selaras-core_backups:/backups --network selaras-core_default \
  -v "$ROOT/deploy/compose/backup/restore-globals.sh:/restore-globals.sh:ro" \
  -e PGHOST=postgres -e PGUSER="$POSTGRES_USER" -e PGPASSWORD="$POSTGRES_PASSWORD" \
  postgres:18.6-alpine sh /restore-globals.sh "$GLOBALS"
docker run --rm -v selaras-core_backups:/backups --network selaras-core_default \
  -e PGPASSWORD="$POSTGRES_PASSWORD" postgres:18.6-alpine \
  pg_restore -h postgres -U "$POSTGRES_USER" -d "$POSTGRES_DB" --no-owner --role="$POSTGRES_USER" --exit-on-error "$LATEST"
log "restored at +$(mark)s"

# Ownership is handed back to the per-service roles: --no-owner makes
# everything belong to admin, and ADR-006 demands that svc_<schema> owns its
# schema.
for s in identity profile assessment coaching chat nutrition dashboard llm; do
  docker exec -i selaras-postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -q -c "
    ALTER SCHEMA $s OWNER TO svc_$s;
    DO \$\$ DECLARE r record; BEGIN
      FOR r IN SELECT c.relname, c.relkind FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
               WHERE n.nspname = '$s' AND c.relkind IN ('r','p','S','v') LOOP
        EXECUTE format('ALTER TABLE %I.%I OWNER TO %I', '$s', r.relname, 'svc_$s');
      END LOOP; END \$\$;"
done
log "ownership restored at +$(mark)s"

AFTER=$(psql_app "SELECT (SELECT count(*) FROM identity.users) || ' users, ' || (SELECT count(*) FROM assessment.risk_assessments) || ' assessments'")
log "after: $AFTER"

# 5. Semuanya dinyalakan lagi.
# shellcheck disable=SC2086
$COMPOSE start $UNITS >/dev/null 2>&1
until curl -sf -o /dev/null http://127.0.0.1:18080/readyz; do sleep 1; done
log "edge ready at +$(mark)s"

log "total: $(mark)s from stop to ready"
cat <<EOF

Sekarang jalankan test e2e dari Windows dan catat hasilnya:
  task test:e2e
EOF
