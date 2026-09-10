#!/usr/bin/env bash
# Deletes the k3d cluster with everything in it.
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
k3d cluster delete selaras
