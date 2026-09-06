#!/usr/bin/env bash
# Chaos F9-12: broker dimatikan paksa saat pekerjaan berjalan.
#
# Yang dibuktikan: nol event hilang. Permintaan yang diterima saat broker mati
# tetap tersimpan di outbox (published_at IS NULL), dan setelah broker kembali
# SELURUH pekerjaan selesai - bukan sebagian.
#
# Jalankan dari WSL:  bash test/chaos/broker.sh
# Hasilnya dibahas di test/chaos/broker.md.

. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

JOBS="${JOBS:-8}"

log "menyiapkan $JOBS akun dengan profil dan penilaian"
declare -a TOKENS SLUGS
for i in $(seq 1 "$JOBS"); do
  t=$(register "broker-$i"); complete_profile "$t"
  TOKENS+=("$t"); SLUGS+=("$(start_assessment "$t")")
done

log "outbox belum terkirim sebelum gangguan: $(unpublished)"

log "MEMATIKAN broker (docker kill selaras-kafka)"
docker kill selaras-kafka >/dev/null
KILLED_AT=$(date +%s)

log "mengantre $JOBS personalisasi SAAT broker mati"
accepted=0
for i in $(seq 0 $((JOBS - 1))); do
  code=$(personalize "${TOKENS[$i]}" "${SLUGS[$i]}")
  [ "$code" = "202" ] && accepted=$((accepted + 1))
done
log "diterima 202: $accepted dari $JOBS (gateway dan service tidak bergantung pada broker untuk menerima)"

sleep 5
held=$(unpublished)
log "outbox menahan $held event selama broker mati"
if [ "$held" -lt "$JOBS" ]; then
  log "GAGAL: outbox seharusnya menahan sedikitnya $JOBS event, ada $held"
  docker start selaras-kafka >/dev/null
  exit 1
fi

log "MENYALAKAN broker kembali"
docker start selaras-kafka >/dev/null

drained=$(wait_until 0 180 unpublished) || exit 1
log "outbox kosong kembali $drained detik setelah broker dinyalakan"

completed=0
for i in $(seq 0 $((JOBS - 1))); do
  if [ "$(wait_until completed 120 personalization_status "${SLUGS[$i]}")" != "" ]; then
    completed=$((completed + 1))
  fi
done
log "personalisasi selesai: $completed dari $JOBS"

dead=$(psql "SELECT count(*) FROM llm.llm_jobs WHERE status IN ('failed','dead') AND created_at > to_timestamp($KILLED_AT)")
log "job LLM gagal/mati sejak gangguan: $dead"

if [ "$completed" -eq "$JOBS" ] && [ "$dead" = "0" ]; then
  log "LULUS: nol event hilang, seluruh $JOBS pekerjaan selesai setelah broker pulih"
else
  log "GAGAL: $completed/$JOBS selesai, $dead job gagal"
  exit 1
fi
