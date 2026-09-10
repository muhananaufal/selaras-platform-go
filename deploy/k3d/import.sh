#!/usr/bin/env bash
# Builds the image of every unit (plus migrate and topics) and imports them
# into the k3d node (F9-04). No registry: local images go straight into the
# node's containerd, and the chart uses pullPolicy Never.
#
# Two images are built in sequence, not nine at once: this machine has four
# cores and an 8 GB WSL ceiling, and building nine at once was once
# OOM-killed halfway (F8 notes).

set -euo pipefail
# k3d, kubectl, and helm are installed in the WSL user's ~/.local/bin.
export PATH="$HOME/.local/bin:$PATH"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TAG="${TAG:-dev}"
REVISION=$(git -C "$ROOT" rev-parse --short HEAD)
UNITS="identity-svc profile-svc assessment-svc coaching-svc chat-svc nutrition-svc dashboard-svc llm-worker edge-gateway migrate topics"

log() { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*"; }

pending=()
for u in $UNITS; do
  log "building selaras/$u:$TAG"
  docker build -q --build-arg UNIT="$u" --build-arg VERSION="$TAG" --build-arg REVISION="$REVISION" \
    -t "selaras/$u:$TAG" "$ROOT" >/dev/null &
  pending+=($!)
  if [ "${#pending[@]}" -ge 2 ]; then
    wait "${pending[0]}"; pending=("${pending[@]:1}")
  fi
done
for p in "${pending[@]}"; do wait "$p"; done

images=""
for u in $UNITS; do images="$images selaras/$u:$TAG"; done
log "importing into k3d cluster selaras"
# shellcheck disable=SC2086
k3d image import -c selaras $images
log "done: $(echo $UNITS | wc -w) images"
