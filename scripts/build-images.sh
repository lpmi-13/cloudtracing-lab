#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"
source "${repo_root}/scripts/lib/apps.sh"
source "${repo_root}/scripts/lib/versions.sh"

image_tag="${IMAGE_TAG:-latest}"
resolve_requested_apps "$@"

for app in "${resolved_apps[@]}"; do
  case "${app}" in
    coach)
      docker build -f docker/Dockerfile.golang --build-arg SERVICE_PATH=./cmd/coach --build-arg BINARY_NAME=coach -t "cloudtracing/coach:${image_tag}" .
      ;;
    edge)
      docker build -f docker/Dockerfile.golang --build-arg SERVICE_PATH=./cmd/edge --build-arg BINARY_NAME=edge -t "cloudtracing/edge:${image_tag}" .
      ;;
    catalog)
      docker build -f docker/Dockerfile.golang --build-arg SERVICE_PATH=./services/catalog --build-arg BINARY_NAME=catalog -t "cloudtracing/catalog:${image_tag}" .
      ;;
    inventory)
      docker build -f docker/Dockerfile.golang --build-arg SERVICE_PATH=./services/inventory --build-arg BINARY_NAME=inventory -t "cloudtracing/inventory:${image_tag}" .
      ;;
    orders)
      docker build -f docker/Dockerfile.golang --build-arg SERVICE_PATH=./services/orders --build-arg BINARY_NAME=orders -t "cloudtracing/orders:${image_tag}" .
      ;;
    shop-web)
      docker build -f docker/Dockerfile.python --build-arg REQUIREMENTS=python/requirements-web.txt --build-arg APP_MODULE=python.web.app -t "cloudtracing/shop-web:${image_tag}" .
      ;;
    payments)
      docker build -f docker/Dockerfile.python --build-arg REQUIREMENTS=python/requirements-payments.txt --build-arg APP_MODULE=python.payments.app -t "cloudtracing/payments:${image_tag}" .
      ;;
    jaeger-ui)
      docker build \
        -f docker/Dockerfile.jaeger-ui \
        --build-arg JAEGER_UI_VERSION="${JAEGER_UI_VERSION}" \
        --build-arg JAEGER_UI_GIT_TAG="${JAEGER_UI_GIT_TAG}" \
        --build-arg JAEGER_UI_COMMIT_SHA="${JAEGER_UI_COMMIT_SHA}" \
        --build-arg JAEGER_UI_SEARCH_MAX_LIMIT="${JAEGER_UI_SEARCH_MAX_LIMIT}" \
        -t "${JAEGER_UI_IMAGE_REPO}:${image_tag}" .
      ;;
  esac
done
