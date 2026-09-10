#!/bin/sh
# Scheduled Postgres backup (F9-30).
#
# Runs in a container with the SAME Postgres image version as the server, so
# pg_dump always matches. Every round:
#
#   1. pg_dumpall --globals-only -> the roles and their passwords (svc_*);
#      without this a restore produces schemas without owners.
#   2. pg_dump -Fc -> the whole database (eight schemas) in the custom format:
#      compressed, and partially restorable.
#   3. pg_restore --list -> verifies the archive is readable. A backup that is
#      not verified is a hope, not a backup.
#   4. Removes archives older than BACKUP_KEEP days.
#
# The output is written to /backups (a volume). Logs go to stdout, one line per
# step, so "did last night's backup run" is answered from the log.
#
# With BACKUP_ONCE=1 it runs once and exits - used by `task backup:now` and the
# recovery drill.

set -eu

: "${PGHOST:?set PGHOST}"
: "${PGUSER:?set PGUSER}"
: "${PGPASSWORD:?set PGPASSWORD}"
: "${PGDATABASE:?set PGDATABASE}"
BACKUP_DIR="${BACKUP_DIR:-/backups}"
BACKUP_INTERVAL="${BACKUP_INTERVAL:-21600}"   # detik; bawaan enam jam
BACKUP_KEEP="${BACKUP_KEEP:-14}"              # hari

log() { printf '%s backup %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

run_once() {
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  globals="$BACKUP_DIR/globals-$stamp.sql"
  dump="$BACKUP_DIR/$PGDATABASE-$stamp.dump"

  log "starting stamp=$stamp"
  pg_dumpall --globals-only > "$globals.tmp"
  mv "$globals.tmp" "$globals"

  pg_dump --format=custom --compress=6 --file="$dump.tmp" "$PGDATABASE"
  mv "$dump.tmp" "$dump"

  # Verification: the archive has to be readable and hold all eight schemas.
  schemas=$(pg_restore --list "$dump" | grep -c ' SCHEMA - ' || true)
  if [ "$schemas" -lt 8 ]; then
    log "FAILED verification: archive lists $schemas schemas, want at least 8"
    return 1
  fi
  size=$(wc -c < "$dump")
  log "verified stamp=$stamp schemas=$schemas bytes=$size file=$dump"

  # Retention by file age.
  find "$BACKUP_DIR" -maxdepth 1 -type f \( -name '*.dump' -o -name 'globals-*.sql' \) -mtime +"$BACKUP_KEEP" -print -delete \
    | while read -r old; do log "retired $old"; done
  log "done stamp=$stamp"
}

mkdir -p "$BACKUP_DIR"

if [ "${BACKUP_ONCE:-0}" = "1" ]; then
  run_once
  exit $?
fi

while true; do
  if ! run_once; then
    log "FAILED; retrying next interval"
  fi
  sleep "$BACKUP_INTERVAL"
done
