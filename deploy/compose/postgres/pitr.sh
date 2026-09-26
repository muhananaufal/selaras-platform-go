#!/bin/sh
# Base backups for point-in-time recovery (docs/runbook/pitr.md).
#
# Runs as the postgres user next to the server, sharing its data directory
# (read-only) and its socket directory: pgBackRest reads the files directly
# and talks to the server over the socket, which is how it runs on a
# database host. The WAL between backups is archived by the server itself
# (archiving-entrypoint.sh).
#
# Every round is a differential backup against the last full one, and every
# PITR_FULL_EVERY-th round (and the first after a start) is a full backup.
# pgBackRest expires what falls outside repo1-retention-full after each
# backup.
#
# With PITR_ONCE=1 it creates the stanza, takes one full backup, and exits.
#
# Every step checks its own result: this script is called in conditions,
# where `set -e` does not apply (see deploy/compose/backup/backup.sh).
set -eu

STANZA=selaras
INTERVAL="${PITR_BACKUP_INTERVAL:-86400}"   # seconds; one day
FULL_EVERY="${PITR_FULL_EVERY:-7}"          # rounds; weekly with the default interval

log() { printf '%s pitr %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

waited=0
until pg_isready -q; do
  waited=$((waited + 1))
  if [ "$waited" -ge 120 ]; then
    log "FAILED: the server was not ready after 120 s"
    exit 1
  fi
  sleep 1
done

# Idempotent: an existing stanza that matches the cluster is accepted.
if ! pgbackrest --stanza="$STANZA" stanza-create; then
  log "FAILED: stanza-create"
  exit 1
fi
log "stanza ready"

round() {
  if ! pgbackrest --stanza="$STANZA" --type="$1" backup; then
    log "FAILED: backup type=$1"
    return 1
  fi
  log "backup done type=$1"
}

if [ "${PITR_ONCE:-0}" = "1" ]; then
  round full || exit 1
  exit 0
fi

n=0
while true; do
  if [ $((n % FULL_EVERY)) -eq 0 ]; then kind="full"; else kind="diff"; fi
  if round "$kind"; then
    n=$((n + 1))
  else
    log "retrying next interval"
  fi
  sleep "$INTERVAL"
done
