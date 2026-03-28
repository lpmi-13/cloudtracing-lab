#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
cd "${repo_root}"

echo "Building local images..."
bash "${script_dir}/build-images.sh"

echo "Pushing images to the local registry..."
bash "${script_dir}/load-images.sh"

echo "Deploying the local k3s overlay..."
bash "${script_dir}/deploy.sh"

echo
echo "Cluster is ready:"
echo "  coach:       http://localhost:9000"
echo "  jaeger:      http://localhost:9002"
echo "  shop:        http://localhost:9001 (optional manual storefront)"
echo "  edge-api:    http://localhost:9003"
echo "  catalog-api: http://localhost:9004"
echo "  inventory:   http://localhost:9005"
echo "  orders-api:  http://localhost:9006"
echo "  payments:    http://localhost:9007"
echo "  meilisearch: http://localhost:9008"
