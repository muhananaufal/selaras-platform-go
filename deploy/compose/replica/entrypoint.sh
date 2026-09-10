#!/bin/bash
# A Postgres read replica through streaming replication (F9-32).
#
# When the data directory is empty, a base copy is taken from the primary
# with pg_basebackup (-R writes standby.signal and primary_conninfo), and
# the server is then started as a hot standby. When data already exists, the
# server is started as it is and resumes the WAL stream from where it
# stopped.
#
# Runs through the official image's entrypoint so file permissions, locale,
# and the `postgres` user are handled the same way as on the primary.
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
  # gosu, not su-exec: the postgres:*-alpine image ships gosu (checked
  # inside the image, not remembered).
  PGPASSWORD="$REPLICATION_PASSWORD" gosu postgres \
    pg_basebackup -h "$PRIMARY_HOST" -U replicator -D "$PGDATA" -Fp -Xs -R -c fast
  echo "replica: base backup done; starting as hot standby"
fi

# hot_standby=on is already the default; -c makes sure the replica refuses
# writes even if someone points a write DSN here.
exec docker-entrypoint.sh postgres -c hot_standby=on
