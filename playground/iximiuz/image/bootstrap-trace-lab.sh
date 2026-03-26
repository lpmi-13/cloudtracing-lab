#!/usr/bin/env bash
set -euo pipefail

repo_root="/opt/cloudtracing"
images_archive="${repo_root}/playground/iximiuz/k3s-images.tar"
state_dir="/var/lib/cloudtracing"
import_marker="${state_dir}/images-imported"

export KUBECONFIG="${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}"

mkdir -p "${state_dir}"

for _ in $(seq 1 90); do
  if kubectl get --raw=/readyz >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

kubectl get --raw=/readyz >/dev/null 2>&1 || {
  echo "k3s API did not become ready in time" >&2
  exit 1
}

if [[ ! -f "${import_marker}" ]]; then
  k3s ctr -n k8s.io images import "${images_archive}"
  touch "${import_marker}"
fi

bash "${repo_root}/scripts/deploy-preloaded-vm.sh"
