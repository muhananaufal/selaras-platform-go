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

# job_row membaca baris llm_jobs milik sebuah penilaian (menurut slug).
#
# Dicari lewat aggregate_id, bukan job_id yang dijawab API: job_id itu adalah
# id event permintaannya, sedangkan worker memberi id sendiri pada barisnya.
# Keduanya bertemu hanya di idempotency_key dan aggregate_id.
job_row() {
  psql "SELECT status || ' attempts=' || attempts || ' error=' || coalesce(left(last_error, 60), '-')
        FROM llm.llm_jobs
        WHERE aggregate_id = (SELECT id::text FROM assessment.risk_assessments WHERE slug = '$1')
        ORDER BY created_at DESC LIMIT 1"
}

# request_flow mendaftar, mengisi profil, membuat penilaian, lalu meminta
# personalisasi; mencetak slug penilaiannya.
request_flow() {
  local token slug
  token=$(register "llm-$1"); complete_profile "$token"; slug=$(start_assessment "$token")
  curl -s -o /dev/null -X PATCH "$BASE_URL/api/v1/risk-assessments/$slug/personalize" \
    -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -d '{}'
  echo "$slug"
}

# ------------------------------------------------------------- 1. flaky
with_fault "flaky=2"
SLUG=$(request_flow flaky)
took=$(wait_until completed 60 personalization_status "$SLUG") || exit 1
log "flaky=2: personalisasi selesai setelah $took detik; job: $(job_row "$SLUG")"

# ------------------------------------------------------------- 2. error
with_fault "error"
SLUG=$(request_flow error)
took=$(wait_until failed 60 personalization_status "$SLUG") || exit 1
log "error: penilaian ditandai failed setelah $took detik; job: $(job_row "$SLUG")"
log "  alasan yang tersimpan (kolom internal; kontrak publik hanya memuat personalization_status): $(psql "SELECT coalesce(personalization_error, '-') FROM assessment.risk_assessments WHERE slug = '$SLUG'")"

# ------------------------------------------------------------- 3. slow
with_fault "slow=15s"
started=$(date +%s%N)
SLUG=$(request_flow slow)
http_ms=$(( ($(date +%s%N) - started) / 1000000 ))
log "slow=15s: seluruh alur HTTP (daftar, profil, penilaian, personalisasi) dijawab dalam ${http_ms} ms"
took=$(wait_until completed 90 personalization_status "$SLUG") || exit 1
log "slow=15s: personalisasi selesai setelah $took detik; job: $(job_row "$SLUG")"

# ------------------------------------------------------------- pulihkan
with_fault ""
log "llm-worker kembali tanpa gangguan"
