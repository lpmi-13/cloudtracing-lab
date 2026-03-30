#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"
source "${repo_root}/scripts/lib/apps.sh"

local_registry="localhost:30300"
image_tag="${IMAGE_TAG:-latest}"
registry_container_name="cloudtracing-registry"
registry_container_image="registry:2"
registry_api_url="http://${local_registry}/v2/"
resolve_requested_apps "$@"

images=()
for app in "${resolved_apps[@]}"; do
  images+=("cloudtracing/${app}:${image_tag}")
done

wait_for_registry() {
  local attempt

  for attempt in {1..20}; do
    if curl -fsS "${registry_api_url}" >/dev/null; then
      return
    fi
    sleep 1
  done

  echo "error: local registry at ${local_registry} did not become ready." >&2
  exit 1
}

ensure_registry_running() {
  if curl -fsS "${registry_api_url}" >/dev/null; then
    return
  fi

  if docker ps -a --format '{{.Names}}' | grep -Fxq "${registry_container_name}"; then
    echo "Starting local registry container ${registry_container_name} on ${local_registry}..."
    docker start "${registry_container_name}" >/dev/null
  else
    echo "Creating local registry container ${registry_container_name} on ${local_registry}..."
    docker run -d \
      --restart unless-stopped \
      -p 30300:5000 \
      --name "${registry_container_name}" \
      "${registry_container_image}" >/dev/null
  fi

  wait_for_registry
}

ensure_source_images_exist() {
  local image

  for image in "${images[@]}"; do
    if ! docker image inspect "${image}" >/dev/null 2>&1; then
      echo "error: Docker image ${image} does not exist." >&2
      echo "run scripts/build-images.sh first." >&2
      exit 1
    fi
  done
}

ensure_registry_running
ensure_source_images_exist

for image in "${images[@]}"; do
  registry_image="${local_registry}/${image}"
  echo "Pushing ${registry_image}..."
  docker tag "${image}" "${registry_image}"
  docker push "${registry_image}"
done
