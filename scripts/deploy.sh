#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

kustomize_dir="${KUSTOMIZE_DIR:-k8s/overlays/local}"
namespace="${TRACE_LAB_NAMESPACE:-trace-lab}"

infra_deployments=(
  postgres
  redis
  meilisearch
  jaeger
  otel-collector
)

app_deployments=(
  catalog-api
  inventory-api
  orders-api
  payments-api
  edge-api
  shop-web
  coach
)

kubectl kustomize --load-restrictor=LoadRestrictionsNone "${kustomize_dir}" | kubectl apply -f -

# Remove legacy search resources after the Meilisearch migration.
kubectl -n "${namespace}" delete deploy/opensearch svc/opensearch configmap/opensearch-config --ignore-not-found

# Force fresh pods so same-tag local images are pulled after each push to the registry.
for deployment in "${app_deployments[@]}"; do
  kubectl -n "${namespace}" rollout restart "deploy/${deployment}"
done

for deployment in "${infra_deployments[@]}"; do
  kubectl -n "${namespace}" rollout status "deploy/${deployment}" --timeout=180s
done

for deployment in "${app_deployments[@]}"; do
  kubectl -n "${namespace}" rollout status "deploy/${deployment}" --timeout=180s
done
