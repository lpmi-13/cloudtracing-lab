#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

image_tag="${IMAGE_TAG:-latest}"

docker build -f docker/Dockerfile.golang --build-arg SERVICE_PATH=./cmd/coach --build-arg BINARY_NAME=coach -t "cloudtracing/coach:${image_tag}" .
docker build -f docker/Dockerfile.golang --build-arg SERVICE_PATH=./cmd/edge --build-arg BINARY_NAME=edge -t "cloudtracing/edge:${image_tag}" .
docker build -f docker/Dockerfile.golang --build-arg SERVICE_PATH=./services/catalog --build-arg BINARY_NAME=catalog -t "cloudtracing/catalog:${image_tag}" .
docker build -f docker/Dockerfile.golang --build-arg SERVICE_PATH=./services/inventory --build-arg BINARY_NAME=inventory -t "cloudtracing/inventory:${image_tag}" .
docker build -f docker/Dockerfile.golang --build-arg SERVICE_PATH=./services/orders --build-arg BINARY_NAME=orders -t "cloudtracing/orders:${image_tag}" .

docker build -f docker/Dockerfile.python --build-arg REQUIREMENTS=python/requirements-web.txt --build-arg APP_MODULE=python.web.app -t "cloudtracing/shop-web:${image_tag}" .
docker build -f docker/Dockerfile.python --build-arg REQUIREMENTS=python/requirements-payments.txt --build-arg APP_MODULE=python.payments.app -t "cloudtracing/payments:${image_tag}" .
