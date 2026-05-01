#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

namespace="${TRACE_LAB_NAMESPACE:-trace-lab}"
delete_timeout="${TRACE_LAB_DELETE_TIMEOUT:-180s}"

duration_to_seconds() {
  local duration="$1"
  local value
  local suffix

  if [[ "${duration}" =~ ^([0-9]+)([smh]?)$ ]]; then
    value="${BASH_REMATCH[1]}"
    suffix="${BASH_REMATCH[2]}"

    case "${suffix:-s}" in
      s)
        echo "${value}"
        ;;
      m)
        echo $((value * 60))
        ;;
      h)
        echo $((value * 3600))
        ;;
    esac
    return 0
  fi

  echo "Unsupported TRACE_LAB_DELETE_TIMEOUT value: ${duration}. Use Ns, Nm, or Nh." >&2
  exit 1
}

namespace_exists() {
  kubectl get namespace "${namespace}" >/dev/null 2>&1
}

list_remaining_namespace_content() {
  local resource

  while IFS= read -r resource; do
    [[ -z "${resource}" ]] && continue
    kubectl -n "${namespace}" get "${resource}" -o name --ignore-not-found 2>/dev/null || true
  done < <(kubectl api-resources --verbs=list --namespaced -o name 2>/dev/null || true)
}

namespace_has_remaining_content() {
  [[ -n "$(list_remaining_namespace_content | head -n 1)" ]]
}

delete_remaining_namespace_content() {
  local resource
  local names

  while IFS= read -r resource; do
    [[ -z "${resource}" ]] && continue
    mapfile -t names < <(kubectl -n "${namespace}" get "${resource}" -o name --ignore-not-found 2>/dev/null || true)
    if (( ${#names[@]} > 0 )); then
      if [[ "${resource}" == "pods" ]]; then
        kubectl -n "${namespace}" delete --wait=false --ignore-not-found --force --grace-period=0 "${names[@]}" >/dev/null 2>&1 || true
      else
        kubectl -n "${namespace}" delete --wait=false --ignore-not-found "${names[@]}" >/dev/null 2>&1 || true
      fi
    fi
  done < <(kubectl api-resources --verbs=list --namespaced -o name 2>/dev/null || true)
}

namespace_condition_status() {
  local condition="$1"
  kubectl get namespace "${namespace}" \
    -o jsonpath="{range .status.conditions[?(@.type=='${condition}')]}{.status}{end}" 2>/dev/null || true
}

namespace_condition_message() {
  local condition="$1"
  kubectl get namespace "${namespace}" \
    -o jsonpath="{range .status.conditions[?(@.type=='${condition}')]}{.message}{end}" 2>/dev/null || true
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
    | kubectl replace --raw "/api/v1/namespaces/${namespace}/finalize" -f - >/dev/null 2>&1 || ! namespace_exists
}

if ! namespace_exists; then
  if ! namespace_has_remaining_content; then
    echo "Namespace ${namespace} is already absent."
    exit 0
  fi

  echo "Namespace ${namespace} is absent but namespaced content is still visible; cleaning up leftovers..."
fi

poll_timeout_seconds="$(duration_to_seconds "${delete_timeout}")"
force_finalize_attempted=0
content_cleanup_attempted=0

if namespace_exists; then
  echo "Deleting namespace ${namespace}..."
  delete_output=""
  if ! delete_output="$(kubectl delete namespace "${namespace}" --wait=false 2>&1)"; then
    if ! namespace_exists && ! namespace_has_remaining_content; then
      echo "Namespace ${namespace} removed."
      exit 0
    fi

    if [[ -n "${delete_output}" ]]; then
      echo "${delete_output}" >&2
    fi
  elif [[ -n "${delete_output}" ]]; then
    echo "${delete_output}"
  fi
fi

deadline=$((SECONDS + poll_timeout_seconds))
while (( SECONDS < deadline )); do
  if ! namespace_exists; then
    if ! namespace_has_remaining_content; then
      echo "Namespace ${namespace} removed."
      exit 0
    fi

    if (( content_cleanup_attempted == 0 )); then
      echo "Namespace ${namespace} is absent but namespaced content is still visible; deleting leftovers..."
      delete_remaining_namespace_content
      content_cleanup_attempted=1
      continue
    fi

    sleep 1
    continue
  fi

  if (( force_finalize_attempted == 0 )) && namespace_safe_to_force_finalize; then
    discovery_message="$(namespace_condition_message NamespaceDeletionDiscoveryFailure)"
    if [[ -n "${discovery_message}" ]]; then
      echo "Kubernetes reported API discovery blockage while deleting ${namespace}:"
      echo "${discovery_message}"
    fi

    echo "All namespace content is already gone; force-finalizing ${namespace}..."
    force_finalize_namespace
    force_finalize_attempted=1
    continue
  fi

  sleep 1
done

if ! namespace_exists && ! namespace_has_remaining_content; then
  echo "Namespace ${namespace} removed."
  exit 0
fi

echo "Namespace ${namespace} is still terminating after ${delete_timeout}; inspecting controller status..."
kubectl get namespace "${namespace}" -o yaml || true
if namespace_has_remaining_content; then
  echo "Remaining namespaced objects in ${namespace}:"
  list_remaining_namespace_content
fi
exit 1
