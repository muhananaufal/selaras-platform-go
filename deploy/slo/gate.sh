#!/usr/bin/env bash
# Release-freeze gate of the error budget policy (docs/runbook/error-budget.md).
#
# Usage: deploy/slo/gate.sh <prometheus-url>
#   BUDGET_OVERRIDE  reason for deploying although a budget is spent (a
#                    reliability or security fix); empty means no override.
#
# Reads the verdict of bin/slo-budget (build it first) and turns it into the
# policy:
#   0 budget left           -> deploy
#   1 budget spent          -> refuse, unless BUDGET_OVERRIDE names a reason
#   2 no verdict            -> deploy with a warning: a fresh cluster or a
#                              Prometheus restart must not block a release,
#                              but it must not pass silently either
#   anything else           -> refuse (usage error, a bug in the gate)
set -uo pipefail
url="${1:?usage: gate.sh <prometheus-url>}"
override="${BUDGET_OVERRIDE:-}"

./bin/slo-budget -prometheus "$url"
code=$?
case "$code" in
  0)
    echo "error budget: ok"
    ;;
  1)
    if [ -n "$override" ]; then
      echo "::warning::error budget spent; deploying under override: $override"
    else
      echo "::error::error budget spent - feature releases are frozen (docs/runbook/error-budget.md). A reliability or security fix is dispatched with budget_override."
      exit 1
    fi
    ;;
  2)
    echo "::warning::no error-budget verdict (Prometheus unreachable, rules not loaded, or no traffic yet); deploying"
    ;;
  *)
    echo "::error::slo-budget exited $code"
    exit "$code"
    ;;
esac
