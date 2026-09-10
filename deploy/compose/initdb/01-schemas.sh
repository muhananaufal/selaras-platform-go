#!/bin/bash
# Creates one schema and one role per service, with fenced-off access
# rights. ADR-006: isolation is enforced by the database engine, not by
# developer discipline. A service that tries to touch its neighbour's schema
# is refused by Postgres, not merely in breach of a convention.
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
    CREATE ROLE svc_${svc} LOGIN PASSWORD '${pw}';

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
  echo "  schema ${svc} + role svc_${svc} created"
done

# Keep any role from creating objects in public.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -c "REVOKE CREATE ON SCHEMA public FROM PUBLIC;"
