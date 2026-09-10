#!/usr/bin/env bash
# Creates two Secrets in the cluster from .env (F9-01, ADR-016).
#
#   selaras-infra   : the Postgres credentials and per-service roles, read by
#                     Postgres (initdb) and PgBouncer.
#   selaras-secrets : what the units need - one DSN per unit, the token keys,
#                     the Gemini key. The key names match the units' environment
#                     variables, and the chart references them through secretEnv.
#
# The chart NEVER holds credentials; it only knows the NAME of the Secret.
# This script is the only place .env crosses into the cluster, and it
# refuses to run when any required variable is empty.

set -euo pipefail
# k3d, kubectl, and helm are installed in the WSL user's ~/.local/bin.
export PATH="$HOME/.local/bin:$PATH"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
set -a; . "$ROOT/.env"; set +a

need() { for v in "$@"; do [ -n "${!v:-}" ] || { echo "FATAL: $v is not set in .env" >&2; exit 1; }; done; }
need POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DB JWT_SIGNING_KEY JWT_VERIFY_KEY LLM_PROVIDER \
  SVC_IDENTITY_PASSWORD SVC_PROFILE_PASSWORD SVC_ASSESSMENT_PASSWORD SVC_COACHING_PASSWORD \
  SVC_CHAT_PASSWORD SVC_NUTRITION_PASSWORD SVC_DASHBOARD_PASSWORD SVC_LLM_PASSWORD
# The Gemini key and model name are required only when the provider is
# Gemini; with the fake provider both are empty and never read (llm-worker
# checks them itself at start, F3-16).
[ "$LLM_PROVIDER" != "gemini" ] || need GEMINI_API_KEY GEMINI_MODEL

kubectl get namespace selaras >/dev/null 2>&1 || kubectl create namespace selaras

# The DSNs point at PgBouncer, not Postgres (F9-27). The search_path in the
# DSN is ignored by PgBouncer; the roles have their default from initdb.
dsn() { echo "postgres://svc_$1:$2@pgbouncer:5432/${POSTGRES_DB}?sslmode=disable"; }
# Migrations go STRAIGHT to Postgres: golang-migrate holds a session-level
# advisory lock, and PgBouncer's transaction mode cuts the session between
# statements - the lock is silently lost.
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
