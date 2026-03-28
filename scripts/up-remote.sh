#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

echo "Publishing images to GHCR..."
if [[ -n "${IMAGE_TAG:-}" ]]; then
  export IMAGE_TAG
  bash "${repo_root}/scripts/publish-ghcr.sh"
else
  image_tag_file="$(mktemp /tmp/cloudtracing-image-tag.XXXXXX)"
  trap 'rm -f "${image_tag_file}"' EXIT
  IMAGE_TAG_OUTPUT_FILE="${image_tag_file}" bash "${repo_root}/scripts/publish-ghcr.sh"
  IMAGE_TAG="$(tr -d '\n' < "${image_tag_file}")"
  if [[ -z "${IMAGE_TAG}" ]]; then
    echo "error: publish-ghcr.sh did not report the selected IMAGE_TAG." >&2
    exit 1
  fi
  export IMAGE_TAG
fi

echo "Deploying the remote overlay with IMAGE_TAG=${IMAGE_TAG}..."
bash "${repo_root}/scripts/deploy-remote.sh"
