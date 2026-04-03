#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"
source "${repo_root}/scripts/lib/versions.sh"

app_image_tag="${IMAGE_TAG:-v1}"
rootfs_image="${ROOTFS_IMAGE:-cloudtracing-k3s-rootfs:v1}"
rootfs_release="${ROOTFS_RELEASE:-e2771a49}"
build_images="${BUILD_IMAGES:-1}"
pull_runtime_images="${PULL_RUNTIME_IMAGES:-1}"
push_image="${PUSH_IMAGE:-0}"

app_images=(
  "cloudtracing/coach:${app_image_tag}"
  "cloudtracing/edge:${app_image_tag}"
  "cloudtracing/catalog:${app_image_tag}"
  "cloudtracing/inventory:${app_image_tag}"
  "cloudtracing/orders:${app_image_tag}"
  "cloudtracing/shop-web:${app_image_tag}"
  "cloudtracing/payments:${app_image_tag}"
  "${JAEGER_UI_IMAGE_REPO}:${app_image_tag}"
)

runtime_images=(
  "${POSTGRES_IMAGE}"
  "${REDIS_IMAGE}"
  "${MEILISEARCH_IMAGE}"
  "${JAEGER_IMAGE}"
)

ensure_image() {
  local image="$1"

  if ! docker image inspect "${image}" >/dev/null 2>&1; then
    echo "error: Docker image ${image} does not exist." >&2
    exit 1
  fi
}

copy_dir() {
  local source="$1"
  local destination_parent="$2"

  mkdir -p "${destination_parent}"
  cp -R "${source}" "${destination_parent}/"
}

if [[ "${build_images}" != "0" ]]; then
  echo "Building application images as ${app_image_tag}..."
  APPS= IMAGE_TAG="${app_image_tag}" bash "${repo_root}/scripts/build-images.sh"
fi

if [[ "${pull_runtime_images}" != "0" ]]; then
  for image in "${runtime_images[@]}"; do
    echo "Pulling ${image}..."
    docker pull "${image}"
  done
fi

for image in "${app_images[@]}"; do
  ensure_image "${image}"
done

for image in "${runtime_images[@]}"; do
  ensure_image "${image}"
done

build_context="$(mktemp -d /tmp/cloudtracing-rootfs-build.XXXXXX)"
trap 'rm -rf "${build_context}"' EXIT

mkdir -p "${build_context}/playground/iximiuz/image" "${build_context}/scripts"
cp "${repo_root}/playground/iximiuz/Dockerfile" "${build_context}/Dockerfile"
cp "${repo_root}/playground/iximiuz/image/bootstrap-trace-lab.sh" \
  "${build_context}/playground/iximiuz/image/bootstrap-trace-lab.sh"
cp "${repo_root}/playground/iximiuz/image/trace-lab-bootstrap.service" \
  "${build_context}/playground/iximiuz/image/trace-lab-bootstrap.service"
cp "${repo_root}/scripts/deploy.sh" "${build_context}/scripts/deploy.sh"
cp "${repo_root}/scripts/deploy-preloaded-vm.sh" "${build_context}/scripts/deploy-preloaded-vm.sh"
copy_dir "${repo_root}/scripts/lib" "${build_context}/scripts"

copy_dir "${repo_root}/k8s" "${build_context}"
copy_dir "${repo_root}/db" "${build_context}"
copy_dir "${repo_root}/scenarios" "${build_context}"

echo "Saving Kubernetes runtime images into the rootfs build context..."
docker save -o "${build_context}/playground/iximiuz/k3s-images.tar" \
  "${app_images[@]}" \
  "${runtime_images[@]}"

echo "Building ${rootfs_image}..."
docker build \
  --build-arg ROOTFS_RELEASE="${rootfs_release}" \
  -t "${rootfs_image}" \
  "${build_context}"

if [[ "${push_image}" != "0" ]]; then
  echo "Pushing ${rootfs_image}..."
  docker push "${rootfs_image}"
fi

echo
echo "Built rootfs image ${rootfs_image}"
if [[ "${push_image}" != "0" ]]; then
  echo "Pushed ${rootfs_image}"
fi
