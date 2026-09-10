#!/usr/bin/env bash
# Chaos F9-13: one service is stopped; the others have to keep serving.
#
# What is measured is the BLAST RADIUS: which endpoints die along with it,
# which survive, and how long recovery takes once the service is started
# again. The results, and how to read them, are in test/chaos/service.md.
#
# Run from WSL: bash test/chaos/service.sh [service-name] Default:
# profile-svc, because it is the one most called by other units.

. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

VICTIM="${1:-profile-svc}"
CONTAINER="selaras-${VICTIM%-svc}"

log "menyiapkan satu akun dengan profil dan penilaian"
TOKEN=$(register "service")
complete_profile "$TOKEN"
SLUG=$(start_assessment "$TOKEN")

# call prints the HTTP code, or TIMEOUT(<curl code>) when the request is not
# answered within ten seconds. The latter is a finding, not a script error: a
# gateway that hangs is worse than a gateway that answers 503.
call() {
  local method="$1" path="$2" body="$3"
  if [ "$method" = "GET" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X GET "$BASE_URL/api/v1$path" \
      -H "Authorization: Bearer $TOKEN" || echo "TIMEOUT($?)"
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X "$method" "$BASE_URL/api/v1$path" \
      -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$body" || echo "TIMEOUT($?)"
  fi
}

# probe prints "METHOD PATH -> code" for a list of endpoints representing
# every unit. What is read is not just "failed or not" but the CODE: a 503
# with the uniform error body is an honest failure; a 500 or a hanging
# connection is not.
probe() {
  log "--- $1"
  while read -r method path body; do
    printf '    %-6s %-40s -> %s\n' "$method" "$path" "$(call "$method" "$path" "$body")"
  done <<EOF
GET /me -
GET /profile -
PATCH /profile {"last_name":"Chaos"}
GET /risk-assessments -
POST /risk-assessments {"has_diabetes":false,"smoking_status":"Perokok aktif","q_exercise":"Jarang","sbp_input_type":"manual","sbp_value":150,"tchol_input_type":"manual","tchol_value":6.2,"hdl_input_type":"manual","hdl_value":1.0}
GET /risk-assessments/$SLUG -
GET /dashboard -
GET /culinary/hub-data -
GET /chat/conversations -
GET /coaching/programs/tidak-ada -
EOF
}

probe "sebelum gangguan (semua service hidup)"

# stop -t 0, not kill: the unless-stopped restart policy restarts a
# container that DIED on its own, but not one that was stopped. What is
# tested here is a service that is absent for a while, not one that springs
# straight back.
log "MEMATIKAN $VICTIM (docker stop -t 0 $CONTAINER)"
docker stop -t 0 "$CONTAINER" >/dev/null
sleep 3

probe "saat $VICTIM mati"

# Registering a new account touches identity -> profile (profile creation);
# this is the most interesting cross-unit path while profile-svc is down.
register_code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST "$BASE_URL/api/v1/register" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"Chaos\",\"email\":\"chaos-during-$(date +%s%N)@user.co\",\"password\":\"$PASSWORD\",\"password_confirmation\":\"$PASSWORD\"}" \
  || echo "TIMEOUT($?)")
printf '    %-6s %-40s -> %s\n' POST /register "$register_code"

log "MENYALAKAN $VICTIM kembali"
docker start "$CONTAINER" >/dev/null

ready() { call GET /profile -; }
took=$(wait_until 200 60 ready) || exit 1
log "GET /profile kembali 200 setelah $took detik"

probe "setelah pulih"
log "selesai; blast radius dibaca dari tiga tabel di atas"
