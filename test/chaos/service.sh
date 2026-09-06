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

# call mencetak kode HTTP, atau TIMEOUT(<kode curl>) bila permintaannya tidak
# dijawab dalam sepuluh detik. Yang kedua adalah temuan, bukan galat skrip:
# gateway yang menggantung lebih buruk daripada gateway yang menjawab 503.
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

# probe mencetak "METHOD PATH -> kode" untuk daftar endpoint yang mewakili
# setiap unit. Yang dibaca bukan hanya "gagal atau tidak", tetapi KODE-nya:
# 503 dengan badan galat yang seragam adalah kegagalan yang jujur; 500 atau
# koneksi menggantung bukan.
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

# stop -t 0, bukan kill: kebijakan restart unless-stopped menyalakan ulang
# container yang MATI sendiri, tetapi tidak yang dihentikan. Yang diuji di
# sini adalah service yang tidak ada selama beberapa saat, bukan yang
# langsung bangkit.
log "MEMATIKAN $VICTIM (docker stop -t 0 $CONTAINER)"
docker stop -t 0 "$CONTAINER" >/dev/null
sleep 3

probe "saat $VICTIM mati"

# Pendaftaran akun baru menyentuh identity -> profile (pembuatan profil);
# ini jalur lintas-unit yang paling menarik saat profile-svc mati.
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
