#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
cd "${repo_root}"

if (($# == 0)) && [[ -z "${APPS:-}" ]]; then
  echo "usage: bash scripts/refresh-local-app.sh <app> [<app> ...]" >&2
  echo "example: bash scripts/refresh-local-app.sh coach" >&2
  echo "valid apps: coach edge catalog inventory orders shop-web payments" >&2
  exit 1
fi

echo "Building selected local images..."
bash "${script_dir}/build-images.sh" "$@"

echo "Pushing selected images to the local registry..."
bash "${script_dir}/load-images.sh" "$@"

echo "Restarting selected deployments in the local cluster..."
RESTART_APP_DEPLOYMENTS=1 bash "${script_dir}/deploy.sh" "$@"

echo
echo "Selected app refresh complete."
