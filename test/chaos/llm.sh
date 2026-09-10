#!/usr/bin/env bash
# Chaos F9-14: the LLM provider is slow, fails occasionally, or always fails.
#
# Three faults are played on the fake provider through LLM_FAKE_FAULT, and
# what is checked is THREE behaviours: the worker retries and then succeeds
# (flaky), a job that will never succeed ends up "dead" and its assessment is
# marked "failed" - not pending forever (error), and the 202 answer stays
# instant even when the provider is slow (slow).
#
# Run from WSL: bash test/chaos/llm.sh The results are discussed in
# test/chaos/llm.md.

. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

COMPOSE="docker compose --env-file $ROOT/.env -f $ROOT/deploy/compose/core.yml -f $ROOT/deploy/compose/apps.yml"

# with_fault restarts llm-worker with the requested fault.
with_fault() {
  log "llm-worker dinyalakan ulang dengan LLM_FAKE_FAULT=$1"
  LLM_FAKE_FAULT="$1" OTEL_COLLECTOR_ENDPOINT="${OTEL_COLLECTOR_ENDPOINT:-}" \
    $COMPOSE up -d --no-build llm-worker >/dev/null 2>&1
  sleep 4
  docker logs --since 10s selaras-llm-worker 2>&1 | grep -o '"msg":"[^"]*FAULT[^"]*","fault":"[^"]*"' || true
}

# job_row reads the llm_jobs row of an assessment (by slug).
#
# Looked up through aggregate_id, not the job_id the API answers with: that
# job_id is the id of the request event, while the worker gives its row an id
# of its own. The two meet only in idempotency_key and aggregate_id.
job_row() {
  psql "SELECT status || ' attempts=' || attempts || ' error=' || coalesce(left(last_error, 60), '-')
        FROM llm.llm_jobs
        WHERE aggregate_id = (SELECT id::text FROM assessment.risk_assessments WHERE slug = '$1')
        ORDER BY created_at DESC LIMIT 1"
}

# request_flow registers, fills in the profile, creates an assessment, then
# requests personalisation; prints the assessment slug.
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
