#!/usr/bin/env bash
# Pustaka bersama skrip chaos. Dijalankan dari WSL, tempat daemon Docker
# hidup, terhadap stack compose lokal.
#
# Yang disediakan: pembacaan .env, kueri ke Postgres lewat container-nya,
# dan alur HTTP minimum (daftar -> profil -> penilaian -> personalisasi)
# supaya setiap skenario memulai dari keadaan yang sama.

set -euo pipefail

CHAOS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$CHAOS_DIR/../.." && pwd)"
BASE_URL="${BASE_URL:-http://127.0.0.1:18080}"
PASSWORD="correct-horse-battery"

# shellcheck disable=SC1091
set -a; . "$ROOT/.env"; set +a

log() { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*"; }

# psql menjalankan satu pernyataan sebagai superuser dan mencetak hasilnya
# tanpa hiasan. Superuser, karena skrip ini membaca lintas skema - hal yang
# sengaja tidak bisa dilakukan peran per-service (ADR-006).
psql() {
  docker exec -i selaras-postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tA -c "$1"
}

# unpublished menghitung baris outbox yang belum terkirim di seluruh skema.
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

# Alur HTTP. Setiap fungsi mencetak yang dibutuhkan langkah berikutnya.
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

# personalize mencetak kode HTTP-nya; 202 berarti pekerjaannya diantre.
personalize() {
  curl -s -o /dev/null -w '%{http_code}' -X PATCH "$BASE_URL/api/v1/risk-assessments/$2/personalize" \
    -H "Authorization: Bearer $1" -H 'Content-Type: application/json' -d '{}'
}

# personalization_status membaca status penilaian menurut slug.
personalization_status() {
  psql "SELECT personalization_status FROM assessment.risk_assessments WHERE slug = '$1'"
}

# wait_until mengulang sebuah perintah sampai keluarannya sama dengan yang
# diharapkan, atau menyerah setelah batas detik. Mencetak berapa lama.
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
