#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

export IMAGE_TAG="${IMAGE_TAG:-$(date -u +%Y%m%d%H%M%S)}"

echo "Publishing images to GHCR..."
bash "${repo_root}/scripts/publish-ghcr.sh"

echo "Deploying the remote overlay..."
bash "${repo_root}/scripts/deploy-remote.sh"
