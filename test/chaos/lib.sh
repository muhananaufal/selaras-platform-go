#!/usr/bin/env bash
# Shared library of the chaos scripts. Run from WSL, where the Docker daemon
# lives, against the local compose stack.
#
# What it provides: reading .env, queries to Postgres through its container,
# and the minimum HTTP flow (register -> profile -> assessment ->
# personalisation) so every scenario starts from the same state.

set -euo pipefail

CHAOS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$CHAOS_DIR/../.." && pwd)"
BASE_URL="${BASE_URL:-http://127.0.0.1:18080}"
PASSWORD="correct-horse-battery"

# shellcheck disable=SC1091
set -a; . "$ROOT/.env"; set +a

log() { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*"; }

# psql runs one statement as the superuser and prints the result without
# decoration. Superuser, because this script reads across schemas -
# something the per-service roles deliberately cannot do (ADR-006).
psql() {
  docker exec -i selaras-postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tA -c "$1"
}

# unpublished counts the outbox rows not yet sent across every schema.
unpublished() {
  psql "SELECT
      (SELECT count(*) FROM identity.outbox   WHERE published_at IS NULL)
    + (SELECT count(*) FROM profile.outbox    WHERE published_at IS NULL)
    + (SELECT count(*) FROM assessment.outbox WHERE published_at IS NULL)
    + (SELECT count(*) FROM coaching.outbox   WHERE published_at IS NULL)
    + (SELECT count(*) FROM chat.outbox       WHERE published_at IS NULL)
    + (SELECT count(*) FROM nutrition.outbox  WHERE published_at IS NULL)
    + (SELECT count(*) FROM dashboard.outbox  WHERE published_at IS NULL)
    + (SELECT count(*) FROM llm.outbox        WHERE published_at IS NULL)"
}

# The HTTP flow. Every function prints what the next step needs.
register() {
  local email="chaos-$1-$(date +%s%N)@user.co"
  curl -sf -X POST "$BASE_URL/api/v1/register" -H 'Content-Type: application/json' \
    -d "{\"name\":\"Chaos\",\"email\":\"$email\",\"password\":\"$PASSWORD\",\"password_confirmation\":\"$PASSWORD\"}" \
    | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'
}

complete_profile() {
  curl -sf -o /dev/null -X PATCH "$BASE_URL/api/v1/profile" \
    -H "Authorization: Bearer $1" -H 'Content-Type: application/json' \
    -d '{"first_name":"Uji","last_name":"Chaos","date_of_birth":"1970-05-10","sex":"male","country_of_residence":"Indonesia"}'
}

start_assessment() {
  curl -sf -X POST "$BASE_URL/api/v1/risk-assessments" \
    -H "Authorization: Bearer $1" -H 'Content-Type: application/json' \
    -d '{"has_diabetes":false,"smoking_status":"Perokok aktif","q_exercise":"Jarang","sbp_input_type":"manual","sbp_value":150,"tchol_input_type":"manual","tchol_value":6.2,"hdl_input_type":"manual","hdl_value":1.0}' \
    | sed -n 's/.*"slug":"\([^"]*\)".*/\1/p'
}

# personalize prints its HTTP code; 202 means the job was queued.
personalize() {
  curl -s -o /dev/null -w '%{http_code}' -X PATCH "$BASE_URL/api/v1/risk-assessments/$2/personalize" \
    -H "Authorization: Bearer $1" -H 'Content-Type: application/json' -d '{}'
}

# personalization_status reads the assessment status by slug.
personalization_status() {
  psql "SELECT personalization_status FROM assessment.risk_assessments WHERE slug = '$1'"
}

# wait_until repeats a command until its output equals what is expected, or
# gives up after the limit in seconds. Prints how long it took.
wait_until() {
  local want="$1" timeout="$2" started; shift 2
  started=$(date +%s)
  while true; do
    if [ "$("$@")" = "$want" ]; then
      echo $(( $(date +%s) - started ))
      return 0
    fi
    if [ $(( $(date +%s) - started )) -ge "$timeout" ]; then
      log "gave up waiting for '$*' to be '$want' (last: $("$@"))"
      return 1
    fi
    sleep 1
  done
}
