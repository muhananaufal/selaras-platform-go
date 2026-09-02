#!/bin/bash
# Membuat satu schema dan satu role per service, dengan hak akses yang
# dipagari. ADR-006: isolasi ditegakkan mesin database, bukan disiplin
# pengembang. Sebuah service yang mencoba menyentuh schema tetangganya
# akan ditolak Postgres, bukan sekadar melanggar konvensi.
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

    -- Hanya schema miliknya sendiri yang terlihat.
    REVOKE ALL ON SCHEMA public FROM svc_${svc};
    GRANT USAGE, CREATE ON SCHEMA ${svc} TO svc_${svc};
    ALTER ROLE svc_${svc} SET search_path TO ${svc};

    -- Berlaku juga untuk tabel yang dibuat migrasi belakangan.
    ALTER DEFAULT PRIVILEGES IN SCHEMA ${svc}
      GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO svc_${svc};
    ALTER DEFAULT PRIVILEGES IN SCHEMA ${svc}
      GRANT USAGE, SELECT ON SEQUENCES TO svc_${svc};
SQL
  echo "  schema ${svc} + role svc_${svc} created"
done

# Cegah role mana pun membuat objek di public.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -c "REVOKE CREATE ON SCHEMA public FROM PUBLIC;"
