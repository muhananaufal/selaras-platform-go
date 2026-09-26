#!/bin/bash
# Creates one schema and one role per service, with fenced-off access
# rights. ADR-006: isolation is enforced by the database engine, not by
# developer discipline. A service that tries to touch its neighbour's schema
# is refused by Postgres, not merely in breach of a convention.
#
# Idempotent: initdb runs it once on a fresh volume, and `task db:provision`
# runs it again on a database that already has everything - the only way a
# unit added later, or a rotated password in .env, reaches a cluster whose
# initdb ran long ago (test/drill/provision.test.sh). Every run sets each
# role's password from the environment.
set -euo pipefail

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
    SELECT format('CREATE ROLE %I LOGIN', 'svc_${svc}')
      WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'svc_${svc}') \gexec
    ALTER ROLE svc_${svc} LOGIN PASSWORD '${pw}';

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
  SELECT 'CREATE ROLE clinic_owner LOGIN'
    WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'clinic_owner') \gexec
  ALTER ROLE clinic_owner LOGIN PASSWORD '${CLINIC_OWNER_PASSWORD}';
  SELECT 'CREATE ROLE svc_clinic LOGIN'
    WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'svc_clinic') \gexec
  ALTER ROLE svc_clinic LOGIN PASSWORD '${SVC_CLINIC_PASSWORD}';

  CREATE SCHEMA IF NOT EXISTS clinic AUTHORIZATION clinic_owner;
  REVOKE ALL ON SCHEMA public FROM clinic_owner, svc_clinic;
  REVOKE CREATE ON SCHEMA clinic FROM svc_clinic;
  GRANT USAGE ON SCHEMA clinic TO svc_clinic;
  ALTER ROLE clinic_owner SET search_path TO clinic;
  ALTER ROLE svc_clinic SET search_path TO clinic;
SQL
echo "  schema clinic (owner clinic_owner) + role svc_clinic ensured"

# Keep any role from creating objects in public.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -c "REVOKE CREATE ON SCHEMA public FROM PUBLIC;"
