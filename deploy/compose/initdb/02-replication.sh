#!/bin/bash
# Peran replikasi untuk replika baca (F9-32).
#
# Berjalan sekali saat initdb, setelah 01-schemas.sh. Peran `replicator`
# hanya boleh REPLICATION dan LOGIN - ia tidak bisa membaca satu pun tabel,
# jadi kata sandinya yang bocor tidak membuka data, hanya aliran WAL.
#
# pg_hba bawaan image hanya memuat `host all all all scram-sha-256`, dan
# koneksi replikasi TIDAK tercakup oleh "all" - ia butuh baris `replication`
# sendiri. Ditambahkan di sini dan dimuat ulang, supaya replika bisa
# menyambung tanpa menyunting berkas di volume secara manual.
set -euo pipefail

if [ -z "${REPLICATION_PASSWORD:-}" ]; then
  echo "FATAL: REPLICATION_PASSWORD is not set. Refusing to create a replication role with a default password." >&2
  exit 1
fi

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-SQL
  CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD '${REPLICATION_PASSWORD}';
SQL

echo "host replication replicator all scram-sha-256" >> "$PGDATA/pg_hba.conf"
# pg_reload_conf lewat psql, bukan pg_ctl: pg_ctl menolak berjalan sebagai
# root, dan skrip ini juga dipakai manual di primer yang sudah ada.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" -c "SELECT pg_reload_conf();" >/dev/null
echo "  replication role and pg_hba entry created"
