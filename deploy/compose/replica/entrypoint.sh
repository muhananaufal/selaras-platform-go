#!/bin/bash
# Replika baca Postgres lewat streaming replication (F9-32).
#
# Saat direktori data kosong, salinan dasar diambil dari primer dengan
# pg_basebackup (-R menulis standby.signal dan primary_conninfo), lalu
# server dinyalakan sebagai hot standby. Saat data sudah ada, server
# dinyalakan apa adanya dan melanjutkan aliran WAL dari tempat ia berhenti.
#
# Berjalan lewat entrypoint image resmi supaya hak berkas, locale, dan
# pengguna `postgres` ditangani cara yang sama dengan primer.
set -euo pipefail

: "${PRIMARY_HOST:?set PRIMARY_HOST}"
: "${REPLICATION_PASSWORD:?set REPLICATION_PASSWORD}"
PGDATA="${PGDATA:-/var/lib/postgresql/data}"

if [ ! -s "$PGDATA/PG_VERSION" ]; then
  echo "replica: taking a base backup from $PRIMARY_HOST"
  until pg_isready -h "$PRIMARY_HOST" -U replicator -q; do sleep 2; done
  mkdir -p "$PGDATA"
  chown postgres:postgres "$PGDATA"
  chmod 700 "$PGDATA"
  PGPASSWORD="$REPLICATION_PASSWORD" su-exec postgres \
    pg_basebackup -h "$PRIMARY_HOST" -U replicator -D "$PGDATA" -Fp -Xs -R -c fast
  echo "replica: base backup done; starting as hot standby"
fi

# hot_standby=on sudah bawaan; -c memastikan replika menolak menulis
# sekalipun seseorang mengarahkan DSN tulis ke sini.
exec docker-entrypoint.sh postgres -c hot_standby=on
