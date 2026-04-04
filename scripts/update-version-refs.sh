#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
target_root="${repo_root}"

app_image_tag=""
rootfs_image=""

usage() {
  cat <<'EOF'
Usage: bash scripts/update-version-refs.sh [flags]

Update checked-in image references so the repo matches the selected release tags.

Flags:
  --app-image-tag <tag>   Update first-party app image tags.
  --rootfs-image <image>  Update the iximiuz rootfs image reference.
  --repo-root <path>      Rewrite a copied tree instead of the main repo.
  -h, --help              Show this help text.
EOF
}

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

normalize_rootfs_image() {
  local image="$1"

  if [[ -z "${image}" ]]; then
    echo "error: rootfs image cannot be empty." >&2
    exit 1
  fi

  image="${image#oci://}"

  if [[ -z "${image}" ]]; then
    echo "error: rootfs image cannot be empty." >&2
    exit 1
  fi

  printf '%s' "${image}"
}

escape_sed_replacement() {
  printf '%s' "$1" | sed -e 's/[&#\\]/\\&/g'
}

while (($# > 0)); do
  case "$1" in
    --app-image-tag)
      shift
      [[ $# -gt 0 ]] || {
        echo "error: --app-image-tag requires a value." >&2
        exit 1
      }
      app_image_tag="$1"
      ;;
    --rootfs-image)
      shift
      [[ $# -gt 0 ]] || {
        echo "error: --rootfs-image requires a value." >&2
        exit 1
      }
      rootfs_image="$1"
      ;;
    --repo-root)
      shift
      [[ $# -gt 0 ]] || {
        echo "error: --repo-root requires a value." >&2
        exit 1
      }
      target_root="$1"
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "error: unknown flag '$1'." >&2
      usage >&2
      exit 1
      ;;
  esac
  shift
done

if [[ -z "${app_image_tag}" && -z "${rootfs_image}" ]]; then
  echo "error: pass at least one of --app-image-tag or --rootfs-image." >&2
  usage >&2
  exit 1
fi

target_root="$(cd "${target_root}" && pwd)"
versions_file="${target_root}/scripts/lib/versions.sh"
applications_file="${target_root}/k8s/base/applications.yaml"
observability_file="${target_root}/k8s/base/observability.yaml"
playground_manifest="${target_root}/playground/iximiuz/manifest.yaml"

test -f "${versions_file}" || {
  echo "error: missing versions file at ${versions_file}." >&2
  exit 1
}

if [[ -n "${app_image_tag}" ]]; then
  validate_image_tag "${app_image_tag}"
  escaped_app_tag="$(escape_sed_replacement "${app_image_tag}")"

  test -f "${applications_file}" || {
    echo "error: missing applications file at ${applications_file}." >&2
    exit 1
  }
  test -f "${observability_file}" || {
    echo "error: missing observability file at ${observability_file}." >&2
    exit 1
  }

  grep -q '^readonly DEFAULT_FIRST_PARTY_IMAGE_TAG=' "${versions_file}" || {
    echo "error: ${versions_file} does not define DEFAULT_FIRST_PARTY_IMAGE_TAG." >&2
    exit 1
  }

  sed -i -E \
    "s#^(readonly DEFAULT_FIRST_PARTY_IMAGE_TAG=\").*(\")#\\1${escaped_app_tag}\\2#" \
    "${versions_file}"

  for image_name in catalog inventory orders payments edge shop-web coach; do
    escaped_image_name="$(escape_sed_replacement "${image_name}")"
    sed -i -E \
      "s#(image:[[:space:]]+cloudtracing/${escaped_image_name}:)[^[:space:]]+#\\1${escaped_app_tag}#g" \
      "${applications_file}"
  done

  sed -i -E \
    "s#(image:[[:space:]]+cloudtracing/jaeger-ui:)[^[:space:]]+#\\1${escaped_app_tag}#g" \
    "${observability_file}"
fi

if [[ -n "${rootfs_image}" ]]; then
  rootfs_image="$(normalize_rootfs_image "${rootfs_image}")"
  escaped_rootfs_image="$(escape_sed_replacement "${rootfs_image}")"
  escaped_manifest_source="$(escape_sed_replacement "oci://${rootfs_image}")"

  test -f "${playground_manifest}" || {
    echo "error: missing playground manifest at ${playground_manifest}." >&2
    exit 1
  }

  grep -q '^readonly DEFAULT_IXIMIUZ_ROOTFS_IMAGE=' "${versions_file}" || {
    echo "error: ${versions_file} does not define DEFAULT_IXIMIUZ_ROOTFS_IMAGE." >&2
    exit 1
  }

  sed -i -E \
    "s#^(readonly DEFAULT_IXIMIUZ_ROOTFS_IMAGE=\").*(\")#\\1${escaped_rootfs_image}\\2#" \
    "${versions_file}"

  sed -i -E \
    "s#(source:[[:space:]]+)oci://[^[:space:]]+#\\1${escaped_manifest_source}#g" \
    "${playground_manifest}"
fi

echo "Updated version references under ${target_root}"
if [[ -n "${app_image_tag}" ]]; then
  echo "  first-party app tag: ${app_image_tag}"
fi
if [[ -n "${rootfs_image}" ]]; then
  echo "  rootfs image: ${rootfs_image}"
fi
