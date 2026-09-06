#!/usr/bin/env bash
# Chaos F9-14: penyedia LLM lambat, sesekali gagal, atau selalu gagal.
#
# Tiga gangguan dimainkan pada penyedia palsu lewat LLM_FAKE_FAULT, dan yang
# diperiksa adalah TIGA perilaku: worker mencoba ulang lalu berhasil (flaky),
# pekerjaan yang tidak akan pernah berhasil berakhir "dead" dan penilaiannya
# ditandai "failed" - bukan pending selamanya (error), dan jawaban 202 tetap
# instan sekalipun penyedianya lambat (slow).
#
# Jalankan dari WSL:  bash test/chaos/llm.sh
# Hasilnya dibahas di test/chaos/llm.md.

. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

COMPOSE="docker compose --env-file $ROOT/.env -f $ROOT/deploy/compose/core.yml -f $ROOT/deploy/compose/apps.yml"

# with_fault menyalakan ulang llm-worker dengan gangguan yang diminta.
with_fault() {
  log "llm-worker dinyalakan ulang dengan LLM_FAKE_FAULT=$1"
  LLM_FAKE_FAULT="$1" OTEL_COLLECTOR_ENDPOINT="${OTEL_COLLECTOR_ENDPOINT:-}" \
    $COMPOSE up -d --no-build llm-worker >/dev/null 2>&1
  sleep 4
  docker logs --since 10s selaras-llm-worker 2>&1 | grep -o '"msg":"[^"]*FAULT[^"]*","fault":"[^"]*"' || true
}

job_row() {
  psql "SELECT status || ' attempts=' || attempts || ' error=' || coalesce(left(last_error, 60), '-') FROM llm.llm_jobs WHERE id = '$1'"
}

request_and_job() {
  local token slug code
  token=$(register "llm-$1"); complete_profile "$token"; slug=$(start_assessment "$token")
  code=$(curl -s -X PATCH "$BASE_URL/api/v1/risk-assessments/$slug/personalize" \
    -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -d '{}')
  echo "$slug $(echo "$code" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p')"
}

# ------------------------------------------------------------- 1. flaky
with_fault "flaky=2"
read -r SLUG JOB <<<"$(request_and_job flaky)"
took=$(wait_until completed 60 personalization_status "$SLUG") || exit 1
log "flaky=2: personalisasi selesai setelah $took detik; job: $(job_row "$JOB")"

# ------------------------------------------------------------- 2. error
with_fault "error"
read -r SLUG JOB <<<"$(request_and_job error)"
took=$(wait_until failed 60 personalization_status "$SLUG") || exit 1
log "error: penilaian ditandai failed setelah $took detik; job: $(job_row "$JOB")"
log "  pesan untuk pengguna: $(psql "SELECT coalesce(personalization_error, '-') FROM assessment.risk_assessments WHERE slug = '$SLUG'")"

# ------------------------------------------------------------- 3. slow
with_fault "slow=15s"
started=$(date +%s%N)
read -r SLUG JOB <<<"$(request_and_job slow)"
http_ms=$(( ($(date +%s%N) - started) / 1000000 ))
log "slow=15s: seluruh alur HTTP (daftar, profil, penilaian, personalisasi) dijawab dalam ${http_ms} ms"
took=$(wait_until completed 90 personalization_status "$SLUG") || exit 1
log "slow=15s: personalisasi selesai setelah $took detik; job: $(job_row "$JOB")"

# ------------------------------------------------------------- pulihkan
with_fault ""
log "llm-worker kembali tanpa gangguan"
