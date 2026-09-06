#!/usr/bin/env bash
# Menghapus klaster k3d beserta seluruh isinya.
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
k3d cluster delete selaras
