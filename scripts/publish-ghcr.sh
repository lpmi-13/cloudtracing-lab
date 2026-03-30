#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

ghcr_namespace="${GHCR_NAMESPACE:-}"
image_tag="${IMAGE_TAG:-}"
mirror_upstream="${MIRROR_UPSTREAM_IMAGES:-1}"
build_images="${BUILD_IMAGES:-1}"
source_image_tag="${SOURCE_IMAGE_TAG:-latest}"
image_tag_output_file="${IMAGE_TAG_OUTPUT_FILE:-}"

if [[ -z "${ghcr_namespace}" ]]; then
  echo "error: GHCR_NAMESPACE must be set, for example ghcr.io/your-user-or-org." >&2
  exit 1
fi

if [[ "${ghcr_namespace}" != ghcr.io/* ]]; then
  echo "error: GHCR_NAMESPACE must start with ghcr.io/." >&2
  exit 1
fi

ghcr_owner="${ghcr_namespace#ghcr.io/}"
if [[ -z "${ghcr_owner}" ]] || [[ "${ghcr_owner}" == "${ghcr_namespace}" ]] || [[ "${ghcr_owner}" == */* ]]; then
  echo "error: GHCR_NAMESPACE must look like ghcr.io/<user-or-org>." >&2
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

validate_image_tag() {
  local tag="$1"

  if [[ -z "${tag}" ]]; then
    echo "error: image tag cannot be empty." >&2
    exit 1
  fi

  if [[ ! "${tag}" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]]; then
    echo "error: invalid image tag '${tag}'. Use Docker tag-safe characters only." >&2
    exit 1
  fi
}

package_scope() {
  local owner_type

  if ! command -v gh >/dev/null 2>&1; then
    echo "error: gh is required to auto-suggest the next GHCR tag. Set IMAGE_TAG explicitly or install gh." >&2
    exit 1
  fi

  if ! owner_type="$(gh api "users/${ghcr_owner}" --jq '.type' 2>/dev/null)"; then
    echo "error: unable to look up GitHub account ${ghcr_owner}. Set IMAGE_TAG explicitly or check gh auth." >&2
    exit 1
  fi

  case "${owner_type}" in
    User)
      echo "users"
      ;;
    Organization)
      echo "orgs"
      ;;
    *)
      echo "error: unsupported GitHub account type '${owner_type}' for ${ghcr_owner}." >&2
      exit 1
      ;;
  esac
}

suggest_next_image_tag() {
  local scope="$1"
  local max_version=0
  local image_name package_name encoded_name tag_list tag

  for image_name in "${app_images[@]}"; do
    package_name="cloudtracing/${image_name}"
    encoded_name="${package_name//\//%2F}"
    tag_list="$(gh api "${scope}/${ghcr_owner}/packages/container/${encoded_name}/versions?per_page=100" --paginate --jq '.[].metadata.container.tags[]?' 2>/dev/null || true)"

    while IFS= read -r tag; do
      [[ -z "${tag}" ]] && continue
      if [[ "${tag}" =~ ^v([0-9]+)$ ]] && (( BASH_REMATCH[1] > max_version )); then
        max_version="${BASH_REMATCH[1]}"
      fi
    done <<< "${tag_list}"
  done

  echo "v$((max_version + 1))"
}

choose_image_tag() {
  local suggested_tag chosen_tag scope

  if [[ -n "${image_tag}" ]]; then
    validate_image_tag "${image_tag}"
    return
  fi

  scope="$(package_scope)"
  suggested_tag="$(suggest_next_image_tag "${scope}")"

  if [[ -t 0 && -t 1 ]]; then
    echo "Suggested GHCR tag: ${suggested_tag}"
    read -r -p "Push tag [${suggested_tag}]: " chosen_tag
    image_tag="${chosen_tag:-${suggested_tag}}"
  else
    image_tag="${suggested_tag}"
    echo "IMAGE_TAG not set; defaulting to ${image_tag}."
  fi

  validate_image_tag "${image_tag}"
}

persist_selected_tag() {
  if [[ -n "${image_tag_output_file}" ]]; then
    printf '%s\n' "${image_tag}" > "${image_tag_output_file}"
  fi
}

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

choose_image_tag
persist_selected_tag

echo "Using GHCR tag ${image_tag}."

if [[ "${build_images}" != "0" ]]; then
  echo "Building application images as ${source_image_tag}..."
  APPS= IMAGE_TAG="${source_image_tag}" bash "${repo_root}/scripts/build-images.sh"
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
fi

echo
echo "Published application images to ${ghcr_namespace}/cloudtracing/*:${image_tag}"
if [[ "${mirror_upstream}" != "0" ]]; then
  echo "Mirrored runtime dependencies to ${ghcr_namespace}/cloudtracing-third-party/*"
fi
echo "Use IMAGE_TAG=${image_tag} when deploying."
