#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

namespace="${TRACE_LAB_NAMESPACE:-trace-lab}"
delete_timeout="${TRACE_LAB_DELETE_TIMEOUT:-180s}"

namespace_exists() {
  kubectl get namespace "${namespace}" >/dev/null 2>&1
}

namespace_condition_status() {
  local condition="$1"
  kubectl get namespace "${namespace}" \
    -o jsonpath="{range .status.conditions[?(@.type=='${condition}')]}{.status}{end}"
}

namespace_condition_message() {
  local condition="$1"
  kubectl get namespace "${namespace}" \
    -o jsonpath="{range .status.conditions[?(@.type=='${condition}')]}{.message}{end}"
}

namespace_safe_to_force_finalize() {
  local phase
  local content_remaining
  local finalizers_remaining
  local content_failure

  phase="$(kubectl get namespace "${namespace}" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  content_remaining="$(namespace_condition_status NamespaceContentRemaining)"
  finalizers_remaining="$(namespace_condition_status NamespaceFinalizersRemaining)"
  content_failure="$(namespace_condition_status NamespaceDeletionContentFailure)"

  [[ "${phase}" == "Terminating" ]] &&
    [[ "${content_remaining}" == "False" ]] &&
    [[ "${finalizers_remaining}" == "False" ]] &&
    [[ "${content_failure}" == "False" ]]
}

force_finalize_namespace() {
  printf '{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"%s"},"spec":{"finalizers":[]}}' "${namespace}" \
    | kubectl replace --raw "/api/v1/namespaces/${namespace}/finalize" -f - >/dev/null
}

if ! namespace_exists; then
  echo "Namespace ${namespace} is already absent."
  exit 0
fi

echo "Deleting namespace ${namespace}..."
if kubectl delete namespace "${namespace}" --wait=true --timeout="${delete_timeout}"; then
  echo "Namespace ${namespace} removed."
  exit 0
fi

if ! namespace_exists; then
  echo "Namespace ${namespace} removed."
  exit 0
fi

echo "Namespace ${namespace} is still terminating after ${delete_timeout}; inspecting controller status..."

for _ in $(seq 1 30); do
  if ! namespace_exists; then
    echo "Namespace ${namespace} removed."
    exit 0
  fi

  if namespace_safe_to_force_finalize; then
    discovery_message="$(namespace_condition_message NamespaceDeletionDiscoveryFailure)"
    if [[ -n "${discovery_message}" ]]; then
      echo "Kubernetes reported API discovery blockage while deleting ${namespace}:"
      echo "${discovery_message}"
    fi

    echo "All namespace content is already gone; force-finalizing ${namespace}..."
    force_finalize_namespace

    for _ in $(seq 1 30); do
      if ! namespace_exists; then
        echo "Namespace ${namespace} removed."
        exit 0
      fi
      sleep 1
    done
    break
  fi

  sleep 1
done

for _ in $(seq 1 5); do
  if ! namespace_exists; then
    echo "Namespace ${namespace} removed."
    exit 0
  fi
  sleep 1
done

echo "Namespace ${namespace} is still present after force-finalization attempt."
kubectl get namespace "${namespace}" -o yaml
exit 1
