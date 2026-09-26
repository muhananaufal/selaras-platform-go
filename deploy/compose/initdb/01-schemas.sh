#!/bin/bash
# Creates one schema and one role per service, with fenced-off access
# rights. ADR-006: isolation is enforced by the database engine, not by
# developer discipline. A service that tries to touch its neighbour's schema
# is refused by Postgres, not merely in breach of a convention.
#
# Idempotent: initdb runs it once on a fresh volume, and `task db:provision`
# runs it again on a database that already has everything - the only way a
# unit added later reaches a cluster whose initdb ran long ago
# (test/drill/provision.test.sh).
#
# A role gets its password when it is created. A rerun does NOT set it
# again unless ROTATE_PASSWORDS=yes: ALTER ROLE ... PASSWORD re-hashes even
# an unchanged password with a new SCRAM salt, and PgBouncer, still holding
# the old secret, then fails every unit's login until it restarts - which is
# what the first version of this script did to the local stack.
set -euo pipefail

# rotate prints the statement that sets role's password, when a rotation was
# asked for, and nothing otherwise.
rotate() {
  if [ "${ROTATE_PASSWORDS:-}" = "yes" ]; then
    printf "ALTER ROLE %s PASSWORD '%s';" "$1" "$2"
  fi
}

SERVICES="identity profile assessment coaching chat nutrition dashboard llm"

for svc in $SERVICES; do
  var="SVC_$(echo "$svc" | tr '[:lower:]' '[:upper:]')_PASSWORD"
  pw="${!var-}"
  if [ -z "$pw" ]; then
    echo "FATAL: $var is not set. Refusing to create a role with a default password." >&2
    exit 1
  fi

  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-SQL
    CREATE SCHEMA IF NOT EXISTS ${svc};
    SELECT format('CREATE ROLE %I LOGIN PASSWORD %L', 'svc_${svc}', '${pw}')
      WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'svc_${svc}') \gexec
    $(rotate "svc_${svc}" "$pw")

    -- Only its own schema is visible.
    REVOKE ALL ON SCHEMA public FROM svc_${svc};
    GRANT USAGE, CREATE ON SCHEMA ${svc} TO svc_${svc};
    ALTER ROLE svc_${svc} SET search_path TO ${svc};

    -- Also applies to tables created by later migrations.
    ALTER DEFAULT PRIVILEGES IN SCHEMA ${svc}
      GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO svc_${svc};
    ALTER DEFAULT PRIVILEGES IN SCHEMA ${svc}
      GRANT USAGE, SELECT ON SEQUENCES TO svc_${svc};
SQL
  echo "  schema ${svc} + role svc_${svc} ensured"
done

# The clinic schema (ADR-030) is owned by a migration role, NOT by the unit's
# runtime role. A table's owner can DISABLE TRIGGER and NO FORCE ROW LEVEL
# SECURITY with DDL, so the append-only consent ledger and access audit, and
# their row level security, are only as strong as a runtime role that does
# not own them. clinic_owner runs the migrations and grants svc_clinic
# exactly what each table allows; svc_clinic cannot create anything.
for var in CLINIC_OWNER_PASSWORD SVC_CLINIC_PASSWORD; do
  if [ -z "${!var-}" ]; then
    echo "FATAL: $var is not set. Refusing to create a role with a default password." >&2
    exit 1
  fi
done
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-SQL
  SELECT format('CREATE ROLE clinic_owner LOGIN PASSWORD %L', '${CLINIC_OWNER_PASSWORD}')
    WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'clinic_owner') \gexec
  $(rotate clinic_owner "$CLINIC_OWNER_PASSWORD")
  SELECT format('CREATE ROLE svc_clinic LOGIN PASSWORD %L', '${SVC_CLINIC_PASSWORD}')
    WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'svc_clinic') \gexec
  $(rotate svc_clinic "$SVC_CLINIC_PASSWORD")

  CREATE SCHEMA IF NOT EXISTS clinic AUTHORIZATION clinic_owner;
  REVOKE ALL ON SCHEMA public FROM clinic_owner, svc_clinic;
  REVOKE CREATE ON SCHEMA clinic FROM svc_clinic;
  GRANT USAGE ON SCHEMA clinic TO svc_clinic;
  ALTER ROLE clinic_owner SET search_path TO clinic;
  ALTER ROLE svc_clinic SET search_path TO clinic;
SQL
echo "  schema clinic (owner clinic_owner) + role svc_clinic ensured"

# OpenFGA keeps its tuples in its own database, owned by its own role
# (ADR-030). Its migrations create their own tables; none of them belongs in
# a unit's schema, and no unit role may connect to it.
if [ -z "${OPENFGA_DB_PASSWORD-}" ]; then
  echo "FATAL: OPENFGA_DB_PASSWORD is not set. Refusing to create a role with a default password." >&2
  exit 1
fi
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-SQL
  SELECT format('CREATE ROLE svc_openfga LOGIN PASSWORD %L', '${OPENFGA_DB_PASSWORD}')
    WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'svc_openfga') \gexec
  $(rotate svc_openfga "$OPENFGA_DB_PASSWORD")
  SELECT 'CREATE DATABASE openfga OWNER svc_openfga'
    WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'openfga') \gexec
SQL

# Every role reaches only its own database. Postgres grants CONNECT to
# PUBLIC by default, which would let svc_openfga into the application
# database and every unit role into openfga.
unit_roles=$(for svc in $SERVICES clinic; do printf 'svc_%s, ' "$svc"; done)
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-SQL
  REVOKE CONNECT ON DATABASE ${POSTGRES_DB} FROM PUBLIC;
  GRANT CONNECT ON DATABASE ${POSTGRES_DB} TO ${unit_roles}clinic_owner;
  REVOKE CONNECT ON DATABASE openfga FROM PUBLIC;
  GRANT CONNECT ON DATABASE openfga TO svc_openfga;
SQL
echo "  database openfga (owner svc_openfga) ensured; CONNECT limited per database"

# Keep any role from creating objects in public.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -c "REVOKE CREATE ON SCHEMA public FROM PUBLIC;"
