#!/bin/bash
# The replication role for the read replica (F9-32).
#
# Runs once during initdb, after 01-schemas.sh. The `replicator` role may
# only REPLICATION and LOGIN - it cannot read a single table, so a leaked
# password opens no data, only the WAL stream.
#
# The image's default pg_hba holds only `host all all all scram-sha-256`,
# and replication connections are NOT covered by "all" - they need their own
# `replication` line. Added here and reloaded, so the replica can connect
# without editing a file on the volume by hand.
set -euo pipefail

if [ -z "${REPLICATION_PASSWORD:-}" ]; then
  echo "FATAL: REPLICATION_PASSWORD is not set. Refusing to create a replication role with a default password." >&2
  exit 1
fi

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-SQL
  CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD '${REPLICATION_PASSWORD}';
SQL

echo "host replication replicator all scram-sha-256" >> "$PGDATA/pg_hba.conf"
# pg_reload_conf through psql, not pg_ctl: pg_ctl refuses to run as root,
# and this script is also used by hand on an existing primary.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" -c "SELECT pg_reload_conf();" >/dev/null
echo "  replication role and pg_hba entry created"
