#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"
source "${repo_root}/scripts/lib/apps.sh"

kustomize_dir="${KUSTOMIZE_DIR:-k8s/overlays/local}"
namespace="${TRACE_LAB_NAMESPACE:-trace-lab}"
restart_app_deployments="${RESTART_APP_DEPLOYMENTS:-auto}"
resolve_requested_apps "$@"

infra_deployments=(
  postgres
  redis
  meilisearch
  jaeger
)

app_deployments=()
for app in "${resolved_apps[@]}"; do
  app_deployments+=("$(deployment_name_for_app "${app}")")
done

cleanup_terminal_pods() {
  if ! kubectl get namespace "${namespace}" >/dev/null 2>&1; then
    return
  fi

  kubectl -n "${namespace}" delete pod --field-selector=status.phase=Failed --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n "${namespace}" delete pod --field-selector=status.phase=Succeeded --ignore-not-found >/dev/null 2>&1 || true
}

report_disk_pressure() {
  local pressured_nodes

  pressured_nodes="$(
    kubectl get nodes \
      -o jsonpath='{range .items[*]}{.metadata.name}{"|"}{range .status.conditions[?(@.type=="DiskPressure")]}{.status}{"|"}{.message}{"\n"}{end}{end}' \
      | awk -F'|' '$2=="True"{print $1 ": " $3}'
  )"

  if [[ -z "${pressured_nodes}" ]]; then
    return 0
  fi

  echo "error: one or more cluster nodes are under DiskPressure:" >&2
  while IFS= read -r line; do
    [[ -z "${line}" ]] && continue
    echo "  ${line}" >&2
  done <<< "${pressured_nodes}"
  echo "free host disk space and rerun the local start flow." >&2
  return 1
}

should_restart_apps() {
  case "${restart_app_deployments}" in
    1|true|yes)
      return 0
      ;;
    0|false|no)
      return 1
      ;;
    auto)
      case "${kustomize_dir}" in
        k8s/overlays/local|*/k8s/overlays/local)
          return 0
          ;;
        *)
          return 1
          ;;
      esac
      ;;
    *)
      echo "error: RESTART_APP_DEPLOYMENTS must be one of auto, 1, 0, true, or false." >&2
      exit 1
      ;;
  esac
}

wait_for_rollout() {
  local deployment="$1"

  if kubectl -n "${namespace}" rollout status "deploy/${deployment}" --timeout=180s; then
    return 0
  fi

  echo "error: rollout for deployment/${deployment} did not finish." >&2
  report_disk_pressure || true
  kubectl -n "${namespace}" get pods -l "app=${deployment}" >&2 || true
  return 1
}

cleanup_terminal_pods
report_disk_pressure

kubectl kustomize --load-restrictor=LoadRestrictionsNone "${kustomize_dir}" | kubectl apply -f -

# Remove legacy search resources after the Meilisearch migration.
kubectl -n "${namespace}" delete deploy/opensearch svc/opensearch configmap/opensearch-config --ignore-not-found

for deployment in "${infra_deployments[@]}"; do
  wait_for_rollout "${deployment}"
done

if should_restart_apps; then
  # Restart sequentially so local hostPort workloads recycle cleanly and failures stop at
  # the deployment that actually broke instead of piling up multiple pending replacements.
  for deployment in "${app_deployments[@]}"; do
    kubectl -n "${namespace}" rollout restart "deploy/${deployment}"
    wait_for_rollout "${deployment}"
  done
else
  for deployment in "${app_deployments[@]}"; do
    wait_for_rollout "${deployment}"
  done
fi
