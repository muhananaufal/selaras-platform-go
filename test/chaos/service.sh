#!/usr/bin/env bash
# Chaos F9-13: satu service dimatikan; yang lain harus tetap melayani.
#
# Yang diukur adalah BLAST RADIUS: endpoint mana yang ikut mati, mana yang
# bertahan, dan berapa lama pemulihannya setelah service dinyalakan kembali.
# Hasilnya, dan pembacaannya, ada di test/chaos/service.md.
#
# Jalankan dari WSL:  bash test/chaos/service.sh [nama-service]
# Bawaan: profile-svc, karena ia yang paling banyak dipanggil unit lain.

. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

VICTIM="${1:-profile-svc}"
CONTAINER="selaras-${VICTIM%-svc}"

log "menyiapkan satu akun dengan profil dan penilaian"
TOKEN=$(register "service")
complete_profile "$TOKEN"
SLUG=$(start_assessment "$TOKEN")

# probe mencetak "METHOD PATH -> kode" untuk daftar endpoint yang mewakili
# setiap unit. Yang dibaca bukan hanya "gagal atau tidak", tetapi KODE-nya:
# 503 dengan badan galat yang seragam adalah kegagalan yang jujur; 500 atau
# koneksi menggantung bukan.
probe() {
  local label="$1"
  log "--- $label"
  while read -r method path body; do
    if [ "$method" = "GET" ]; then
      code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X GET "$BASE_URL/api/v1$path" -H "Authorization: Bearer $TOKEN")
    else
      code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X "$method" "$BASE_URL/api/v1$path" \
        -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$body")
    fi
    printf '    %-6s %-40s -> %s\n' "$method" "$path" "$code"
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

log "MEMATIKAN $VICTIM (docker kill $CONTAINER)"
docker kill "$CONTAINER" >/dev/null
sleep 3

probe "saat $VICTIM mati"

# Pendaftaran akun baru menyentuh identity -> profile (pembuatan profil);
# ini jalur lintas-unit yang paling menarik saat profile-svc mati.
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE_URL/api/v1/register" -H 'Content-Type: application/json' \
  -d "{\"name\":\"Chaos\",\"email\":\"chaos-during-$(date +%s%N)@user.co\",\"password\":\"$PASSWORD\",\"password_confirmation\":\"$PASSWORD\"}")
printf '    %-6s %-40s -> %s\n' POST /register "$code"

log "MENYALAKAN $VICTIM kembali"
docker start "$CONTAINER" >/dev/null
STARTED=$(date +%s)

ready() { curl -s -o /dev/null -w '%{http_code}' --max-time 3 -X GET "$BASE_URL/api/v1/profile" -H "Authorization: Bearer $TOKEN"; }
took=$(wait_until 200 60 ready) || exit 1
log "GET /profile kembali 200 setelah $took detik"

probe "setelah pulih"
log "selesai; blast radius dibaca dari tiga tabel di atas"
