#!/bin/sh
# Restores roles from a `pg_dumpall --globals-only` file.
#
# Usage: restore-globals.sh <globals.sql>   (connection from PG* variables)
#
# The roles usually exist already (the cluster was not dropped, only the
# database), so every CREATE ROLE fails with "already exists" - and that one
# error, and only that one, is expected. Everything else is a failed restore:
# an ALTER ROLE that did not run leaves a role with the wrong password, and
# the units cannot log in after an apparently successful drill. A missing
# file is a failure too; restoring no roles at all must not look like
# restoring them.
set -eu

file="${1:?usage: restore-globals.sh <globals.sql>}"
[ -r "$file" ] || { echo "globals file not readable: $file" >&2; exit 1; }
# An empty or role-less file restores nothing and would still "succeed".
grep -q '^CREATE ROLE ' "$file" || { echo "globals file holds no roles: $file" >&2; exit 1; }

# Without ON_ERROR_STOP psql runs every statement and reports each error on
# stderr, which is what lets the expected ones be told apart from the rest.
# Its exit code still reports a failed connection or an unreadable file.
errors=$(psql -X -q -d postgres -f "$file" 2>&1 >/dev/null) || {
  echo "psql could not apply $file:" >&2
  printf '%s\n' "$errors" >&2
  exit 1
}

unexpected=$(printf '%s\n' "$errors" | grep 'ERROR:' | grep -v 'ERROR:  role ".*" already exists' || true)
if [ -n "$unexpected" ]; then
  echo "restoring roles from $file failed:" >&2
  printf '%s\n' "$unexpected" >&2
  exit 1
fi

tolerated=$(printf '%s\n' "$errors" | grep -c 'already exists' || true)
echo "roles restored from $file ($tolerated already existed)"
