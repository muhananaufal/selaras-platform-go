#!/usr/bin/env bash
# Membuat dua Secret di klaster dari .env (F9-01, ADR-016).
#
#   selaras-infra   : kredensial Postgres dan peran per-service, dibaca
#                     Postgres (initdb) dan PgBouncer.
#   selaras-secrets : yang dibutuhkan unit - satu DSN per unit, kunci token,
#                     kunci Gemini. Nama kuncinya sama dengan variabel
#                     lingkungan unit, dan chart merujuknya lewat secretEnv.
#
# Chart TIDAK pernah memuat kredensial; ia hanya tahu NAMA Secret-nya. Skrip
# ini adalah satu-satunya tempat .env menyeberang ke klaster, dan ia
# menolak berjalan bila ada variabel wajib yang kosong.

set -euo pipefail
# k3d, kubectl, dan helm dipasang di ~/.local/bin milik pengguna WSL.
export PATH="$HOME/.local/bin:$PATH"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
set -a; . "$ROOT/.env"; set +a

need() { for v in "$@"; do [ -n "${!v:-}" ] || { echo "FATAL: $v is not set in .env" >&2; exit 1; }; done; }
need POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DB JWT_SIGNING_KEY JWT_VERIFY_KEY LLM_PROVIDER \
  SVC_IDENTITY_PASSWORD SVC_PROFILE_PASSWORD SVC_ASSESSMENT_PASSWORD SVC_COACHING_PASSWORD \
  SVC_CHAT_PASSWORD SVC_NUTRITION_PASSWORD SVC_DASHBOARD_PASSWORD SVC_LLM_PASSWORD
# Kunci dan nama model Gemini hanya wajib bila penyedianya Gemini; dengan
# penyedia palsu keduanya kosong dan tidak pernah dibaca (llm-worker
# memeriksanya sendiri saat start, F3-16).
[ "$LLM_PROVIDER" != "gemini" ] || need GEMINI_API_KEY GEMINI_MODEL

kubectl get namespace selaras >/dev/null 2>&1 || kubectl create namespace selaras

# DSN menunjuk ke PgBouncer, bukan Postgres (F9-27). search_path di DSN
# diabaikan PgBouncer; peran punya bawaannya dari initdb.
dsn() { echo "postgres://svc_$1:$2@pgbouncer:5432/${POSTGRES_DB}?sslmode=disable"; }
# Migrasi LANGSUNG ke Postgres: golang-migrate memegang advisory lock
# tingkat sesi, dan mode transaksi PgBouncer memutus sesi di antara
# pernyataan - kuncinya hilang diam-diam.
direct() { echo "postgres://svc_$1:$2@postgres:5432/${POSTGRES_DB}?sslmode=disable&search_path=$1"; }

kubectl -n selaras create secret generic selaras-infra \
  --from-literal=POSTGRES_USER="$POSTGRES_USER" \
  --from-literal=POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
  --from-literal=POSTGRES_DB="$POSTGRES_DB" \
  --from-literal=SVC_IDENTITY_PASSWORD="$SVC_IDENTITY_PASSWORD" \
  --from-literal=SVC_PROFILE_PASSWORD="$SVC_PROFILE_PASSWORD" \
  --from-literal=SVC_ASSESSMENT_PASSWORD="$SVC_ASSESSMENT_PASSWORD" \
  --from-literal=SVC_COACHING_PASSWORD="$SVC_COACHING_PASSWORD" \
  --from-literal=SVC_CHAT_PASSWORD="$SVC_CHAT_PASSWORD" \
  --from-literal=SVC_NUTRITION_PASSWORD="$SVC_NUTRITION_PASSWORD" \
  --from-literal=SVC_DASHBOARD_PASSWORD="$SVC_DASHBOARD_PASSWORD" \
  --from-literal=SVC_LLM_PASSWORD="$SVC_LLM_PASSWORD" \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl -n selaras create secret generic selaras-secrets \
  --from-literal=IDENTITY_DATABASE_DSN="$(dsn identity "$SVC_IDENTITY_PASSWORD")" \
  --from-literal=PROFILE_DATABASE_DSN="$(dsn profile "$SVC_PROFILE_PASSWORD")" \
  --from-literal=ASSESSMENT_DATABASE_DSN="$(dsn assessment "$SVC_ASSESSMENT_PASSWORD")" \
  --from-literal=COACHING_DATABASE_DSN="$(dsn coaching "$SVC_COACHING_PASSWORD")" \
  --from-literal=CHAT_DATABASE_DSN="$(dsn chat "$SVC_CHAT_PASSWORD")" \
  --from-literal=NUTRITION_DATABASE_DSN="$(dsn nutrition "$SVC_NUTRITION_PASSWORD")" \
  --from-literal=DASHBOARD_DATABASE_DSN="$(dsn dashboard "$SVC_DASHBOARD_PASSWORD")" \
  --from-literal=LLM_POSTGRES_DSN="$(dsn llm "$SVC_LLM_PASSWORD")" \
  --from-literal=MIGRATE_DSN_IDENTITY="$(direct identity "$SVC_IDENTITY_PASSWORD")" \
  --from-literal=MIGRATE_DSN_PROFILE="$(direct profile "$SVC_PROFILE_PASSWORD")" \
  --from-literal=MIGRATE_DSN_ASSESSMENT="$(direct assessment "$SVC_ASSESSMENT_PASSWORD")" \
  --from-literal=MIGRATE_DSN_COACHING="$(direct coaching "$SVC_COACHING_PASSWORD")" \
  --from-literal=MIGRATE_DSN_CHAT="$(direct chat "$SVC_CHAT_PASSWORD")" \
  --from-literal=MIGRATE_DSN_NUTRITION="$(direct nutrition "$SVC_NUTRITION_PASSWORD")" \
  --from-literal=MIGRATE_DSN_DASHBOARD="$(direct dashboard "$SVC_DASHBOARD_PASSWORD")" \
  --from-literal=MIGRATE_DSN_LLM="$(direct llm "$SVC_LLM_PASSWORD")" \
  --from-literal=JWT_SIGNING_KEY="$JWT_SIGNING_KEY" \
  --from-literal=JWT_VERIFY_KEY="$JWT_VERIFY_KEY" \
  --from-literal=GEMINI_API_KEY="${GEMINI_API_KEY:-}" \
  --from-literal=GEMINI_MODEL="${GEMINI_MODEL:-}" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "secrets applied to namespace selaras (values not shown)"
