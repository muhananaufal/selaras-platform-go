#!/bin/sh
# Starts the server with its WAL archived to pgBackRest. Any command other
# than `postgres` is passed to the official entrypoint unchanged.
#
# archive_timeout bounds how much committed work can be lost at idle: a
# segment with any WAL in it is closed and archived at least once a minute.
set -eu

if [ "${1:-}" = "postgres" ]; then
  shift
  exec docker-entrypoint.sh postgres \
    -c archive_mode=on \
    -c archive_timeout=60 \
    -c "archive_command=pgbackrest --stanza=selaras archive-push %p" \
    "$@"
fi
exec docker-entrypoint.sh "$@"
