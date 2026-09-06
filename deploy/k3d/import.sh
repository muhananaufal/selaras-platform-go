#!/usr/bin/env bash
# Membangun image setiap unit (plus migrate dan topics) dan mengimpornya
# ke node k3d (F9-04). Tanpa registri: image lokal masuk langsung ke
# containerd node, dan chart memakai pullPolicy Never.
#
# Dua image dibangun berurutan, bukan sembilan sekaligus: mesin ini empat
# core dan plafon WSL 8 GB, dan membangun sembilan sekaligus pernah
# di-OOM-kill di tengah (catatan F8).

set -euo pipefail
# k3d, kubectl, dan helm dipasang di ~/.local/bin milik pengguna WSL.
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
