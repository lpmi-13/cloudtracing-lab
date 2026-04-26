#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
cd "${repo_root}"

coach_ui_autostart="${COACH_UI_AUTOSTART:-1}"

echo "Tearing down the existing local trace-lab namespace..."
bash "${script_dir}/down.sh"

echo "Building local images..."
APPS= bash "${script_dir}/build-images.sh"

echo "Pushing images to the local registry..."
APPS= bash "${script_dir}/load-images.sh"

echo "Deploying the local k3s overlay..."
APPS= bash "${script_dir}/deploy.sh"

if [[ "${coach_ui_autostart}" != "0" ]]; then
  echo "Ensuring the coach UI dev server is running..."
  COACH_BACKEND_URL="${COACH_BACKEND_URL:-http://127.0.0.1:9000}" \
  COACH_UI_PORT="${COACH_UI_PORT:-5173}" \
  bash "${script_dir}/coach-ui-dev.sh" --ensure
fi

echo
echo "Cluster is ready:"
if [[ "${coach_ui_autostart}" != "0" ]]; then
  echo "  coach-dev:   http://127.0.0.1:${COACH_UI_PORT:-5173}"
fi
echo "  coach:       http://localhost:9000"
echo "  jaeger:      http://localhost:9002"
echo "  shop:        http://localhost:9001 (optional manual storefront)"
echo "  edge-api:    http://localhost:9003"
echo "  catalog-api: http://localhost:9004"
echo "  inventory:   http://localhost:9005"
echo "  orders-api:  http://localhost:9006"
echo "  payments:    http://localhost:9007"
echo "  meilisearch: http://localhost:9008"
