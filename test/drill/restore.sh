#!/usr/bin/env bash
# Latihan pemulihan dari nol (F9-31, ADR-016, kriteria selesai #14).
#
# Basis data DIHAPUS TOTAL lalu dipulihkan dari backup terakhir di volume
# backups. Setiap langkah diberi waktu, dan skrip ini TIDAK menjalankan test
# e2e sendiri - ia mencetak perintahnya di akhir, dijalankan dari Windows
# (toolchain Go ada di sana), dan hasilnya dicatat di
# docs/runbook/restore-drill.md bersama waktunya.
#
# Jalankan dari WSL:  bash test/drill/restore.sh
#
# Ini MERUSAK: seluruh data lokal diganti dengan isi backup. Itu memang
# intinya. Ia menolak berjalan tanpa DRILL=yes.

set -euo pipefail

[ "${DRILL:-}" = "yes" ] || { echo "refusing: this drops the database. Run with DRILL=yes."; exit 2; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
set -a; . "$ROOT/.env"; set +a
COMPOSE="docker compose --env-file $ROOT/.env -f $ROOT/deploy/compose/core.yml -f $ROOT/deploy/compose/apps.yml -f $ROOT/deploy/compose/full.yml -f $ROOT/deploy/compose/backup.yml"
UNITS="identity-svc profile-svc assessment-svc coaching-svc chat-svc nutrition-svc dashboard-svc llm-worker edge-gateway pgbouncer backup"

log() { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*"; }
psql_admin() { docker exec -i selaras-postgres psql -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 -tA -c "$1"; }
started=$(date +%s)
mark() { echo $(( $(date +%s) - started )); }

# 1. Backup terakhir dipilih SEBELUM apa pun dihapus.
LATEST=$(docker run --rm -v selaras-core_backups:/backups alpine sh -c 'ls -1 /backups/*.dump | sort | tail -1')
GLOBALS=$(docker run --rm -v selaras-core_backups:/backups alpine sh -c 'ls -1 /backups/globals-*.sql | sort | tail -1')
[ -n "$LATEST" ] || { log "no backup found in the backups volume"; exit 1; }
log "restoring from $LATEST (+ $GLOBALS)"

# Angka sebelum: dipakai membandingkan setelah pulih.
BEFORE=$(psql_admin "SELECT (SELECT count(*) FROM identity.users) || ' users, ' || (SELECT count(*) FROM assessment.risk_assessments) || ' assessments'" 2>/dev/null || echo "unknown")
log "before: $BEFORE"

# 2. Semua yang memegang koneksi dimatikan.
log "stopping units and pgbouncer"
# shellcheck disable=SC2086
$COMPOSE stop $UNITS >/dev/null 2>&1
log "stopped at +$(mark)s"

# 3. Basis data dihapus TOTAL.
psql_admin "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$POSTGRES_DB' AND pid <> pg_backend_pid()" >/dev/null
psql_admin "DROP DATABASE $POSTGRES_DB"
log "database dropped at +$(mark)s"
psql_admin "CREATE DATABASE $POSTGRES_DB"

# 4. Peran (sudah ada; galat 'already exists' diabaikan dengan sengaja),
#    lalu isi.
docker run --rm -v selaras-core_backups:/backups --network selaras-core_default \
  -e PGPASSWORD="$POSTGRES_PASSWORD" postgres:18.6-alpine \
  psql -h postgres -U "$POSTGRES_USER" -d postgres -f "$GLOBALS" >/dev/null 2>&1 || true
docker run --rm -v selaras-core_backups:/backups --network selaras-core_default \
  -e PGPASSWORD="$POSTGRES_PASSWORD" postgres:18.6-alpine \
  pg_restore -h postgres -U "$POSTGRES_USER" -d "$POSTGRES_DB" --no-owner --role="$POSTGRES_USER" --exit-on-error "$LATEST"
log "restored at +$(mark)s"

# Pemilik dikembalikan ke peran per-service: --no-owner membuat semuanya
# milik admin, dan ADR-006 menuntut svc_<skema> memiliki skemanya.
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

AFTER=$(psql_admin "SELECT (SELECT count(*) FROM identity.users) || ' users, ' || (SELECT count(*) FROM assessment.risk_assessments) || ' assessments'")
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
