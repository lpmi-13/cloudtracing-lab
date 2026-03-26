#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

ghcr_namespace="${GHCR_NAMESPACE:-}"
image_tag="${IMAGE_TAG:-$(date -u +%Y%m%d%H%M%S)}"
mirror_upstream="${MIRROR_UPSTREAM_IMAGES:-1}"
build_images="${BUILD_IMAGES:-1}"
source_image_tag="${SOURCE_IMAGE_TAG:-v1}"

if [[ -z "${ghcr_namespace}" ]]; then
  echo "error: GHCR_NAMESPACE must be set, for example ghcr.io/your-user-or-org." >&2
  exit 1
fi

if [[ "${ghcr_namespace}" != ghcr.io/* ]]; then
  echo "error: GHCR_NAMESPACE must start with ghcr.io/." >&2
  exit 1
fi

app_images=(
  coach
  edge
  catalog
  inventory
  orders
  shop-web
  payments
)

ensure_local_image() {
  local image="$1"

  if ! docker image inspect "${image}" >/dev/null 2>&1; then
    echo "error: Docker image ${image} does not exist." >&2
    echo "run scripts/build-images.sh first, or leave BUILD_IMAGES=1." >&2
    exit 1
  fi
}

publish_local_image() {
  local source_image="$1"
  local target_image="$2"

  ensure_local_image "${source_image}"
  echo "Pushing ${target_image}..."
  docker tag "${source_image}" "${target_image}"
  docker push "${target_image}"
}

mirror_upstream_image() {
  local source_image="$1"
  local target_image="$2"

  echo "Mirroring ${source_image} -> ${target_image}..."
  docker pull "${source_image}"
  docker tag "${source_image}" "${target_image}"
  docker push "${target_image}"
}

if [[ "${build_images}" != "0" ]]; then
  echo "Building application images..."
  bash "${repo_root}/scripts/build-images.sh"
fi

for image_name in "${app_images[@]}"; do
  publish_local_image \
    "cloudtracing/${image_name}:${source_image_tag}" \
    "${ghcr_namespace}/cloudtracing/${image_name}:${image_tag}"
done

if [[ "${mirror_upstream}" != "0" ]]; then
  mirror_upstream_image "postgres:17.4-alpine" "${ghcr_namespace}/cloudtracing-third-party/postgres:17.4-alpine"
  mirror_upstream_image "redis:8.4.0-alpine" "${ghcr_namespace}/cloudtracing-third-party/redis:8.4.0-alpine"
  mirror_upstream_image "getmeili/meilisearch:v1.15" "${ghcr_namespace}/cloudtracing-third-party/meilisearch:v1.15"
  mirror_upstream_image "jaegertracing/all-in-one:1.75.0" "${ghcr_namespace}/cloudtracing-third-party/jaeger-all-in-one:1.75.0"
  mirror_upstream_image "otel/opentelemetry-collector-contrib:0.143.0" "${ghcr_namespace}/cloudtracing-third-party/opentelemetry-collector-contrib:0.143.0"
fi

echo
echo "Published application images to ${ghcr_namespace}/cloudtracing/*:${image_tag}"
if [[ "${mirror_upstream}" != "0" ]]; then
  echo "Mirrored runtime dependencies to ${ghcr_namespace}/cloudtracing-third-party/*"
fi
echo "Use IMAGE_TAG=${image_tag} when deploying."
