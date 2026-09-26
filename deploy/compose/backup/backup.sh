#!/bin/sh
# Scheduled Postgres backup (F9-30).
#
# Runs in a container with the SAME Postgres image version as the server, so
# pg_dump always matches. Every round:
#
#   1. pg_dumpall --globals-only -> the roles and their passwords (svc_*);
#      without this a restore produces schemas without owners.
#   2. pg_dump -Fc -> the whole database (every unit schema) in the custom format:
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

# The unit roles a role file must carry to be restorable, and the one role
# that is not a unit's runtime role: clinic_owner owns the clinic schema
# (ADR-030), and a restore without it has nobody to give the schema back to.
UNITS="identity profile assessment coaching chat nutrition dashboard llm clinic"
OTHER_ROLES="clinic_owner"

# fail removes this round's temporary files and reports why it failed; the
# caller returns 1 itself.
fail() {
  rm -f "$globals.tmp" "$dump.tmp"
  log "FAILED stamp=$stamp: $*"
}

# run_once checks every step itself instead of relying on `set -e`: in the
# service loop it is called from `if ! run_once`, and POSIX sh ignores
# errexit inside a function called from a condition. Relying on it wrote
# empty files under final names whenever pg_dump failed - seven empty dumps
# and five empty role files that looked like backups. Both files are written
# under a .tmp name and get their final names only after BOTH are verified.
run_once() {
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  globals="$BACKUP_DIR/globals-$stamp.sql"
  dump="$BACKUP_DIR/$PGDATABASE-$stamp.dump"

  log "starting stamp=$stamp"
  pg_dumpall --globals-only > "$globals.tmp" || { fail "pg_dumpall --globals-only"; return 1; }
  pg_dump --format=custom --compress=6 --file="$dump.tmp" "$PGDATABASE" || { fail "pg_dump"; return 1; }

  # Verification: the archive has to be readable and hold every unit's schema,
  # and the role file has to carry every unit role - a dump restores into
  # schemas nobody can log in to without them.
  listing=$(pg_restore --list "$dump.tmp") || { fail "pg_restore --list cannot read the archive"; return 1; }
  schemas=$(printf '%s\n' "$listing" | grep -c ' SCHEMA - ' || true)
  want=$(echo $UNITS | wc -w)
  [ "$schemas" -ge "$want" ] || { fail "archive lists $schemas schemas, want at least $want"; return 1; }
  roles=0
  for role in $(for unit in $UNITS; do echo "svc_$unit"; done) $OTHER_ROLES; do
    if grep -q "^CREATE ROLE $role;" "$globals.tmp"; then
      roles=$((roles + 1))
    else
      fail "role file lacks $role"; return 1
    fi
  done

  mv "$globals.tmp" "$globals" || { fail "naming the role file"; return 1; }
  mv "$dump.tmp" "$dump" || { rm -f "$globals"; fail "naming the archive"; return 1; }
  size=$(wc -c < "$dump")
  log "verified stamp=$stamp schemas=$schemas roles=$roles bytes=$size file=$dump"

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
