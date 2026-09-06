#!/bin/sh
# Backup Postgres terjadwal (F9-30).
#
# Berjalan di container dengan image Postgres yang SAMA versinya dengan
# server, supaya pg_dump selalu sepadan. Setiap putaran:
#
#   1. pg_dumpall --globals-only  -> peran dan kata sandinya (svc_*), tanpa
#      ini pemulihan menghasilkan skema tanpa pemilik.
#   2. pg_dump -Fc                -> seluruh basis data (delapan skema) dalam
#      format custom: terkompresi, dan bisa dipulihkan sebagian.
#   3. pg_restore --list          -> memverifikasi arsipnya bisa dibaca. Backup
#      yang tidak diverifikasi adalah harapan, bukan backup.
#   4. Membuang arsip yang lebih tua dari BACKUP_KEEP hari.
#
# Hasilnya ditulis ke /backups (volume). Log ke stdout, satu baris per
# langkah, supaya "apakah backup semalam berjalan" terjawab dari log.
#
# Dengan BACKUP_ONCE=1 ia berjalan sekali lalu keluar - dipakai `task
# backup:now` dan latihan pemulihan.

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

  # Verifikasi: arsip harus bisa dibaca dan memuat kedelapan skema.
  schemas=$(pg_restore --list "$dump" | grep -c ' SCHEMA - ' || true)
  if [ "$schemas" -lt 8 ]; then
    log "FAILED verification: archive lists $schemas schemas, want at least 8"
    return 1
  fi
  size=$(wc -c < "$dump")
  log "verified stamp=$stamp schemas=$schemas bytes=$size file=$dump"

  # Retensi menurut umur berkas.
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
