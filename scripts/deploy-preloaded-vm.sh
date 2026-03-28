#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

namespace="${TRACE_LAB_NAMESPACE:-trace-lab}"
echo "Deploying the preloaded VM overlay with fixed public host ports..."
KUSTOMIZE_DIR="k8s/overlays/vm" TRACE_LAB_NAMESPACE="${namespace}" bash "${repo_root}/scripts/deploy.sh"

echo
echo "Cluster is ready:"
echo "  coach:  http://127.0.0.1:30080"
echo "  jaeger: http://127.0.0.1:30686"
echo "  shop:   http://127.0.0.1:30081 (optional manual storefront)"
