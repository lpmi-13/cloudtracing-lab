#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"
source "${repo_root}/scripts/lib/versions.sh"

ghcr_namespace="${GHCR_NAMESPACE:-}"
image_tag="${IMAGE_TAG:-}"
base_domain="${TRACE_LAB_BASE_DOMAIN:-}"
mirror_upstream="${MIRROR_UPSTREAM_IMAGES:-1}"
namespace="${TRACE_LAB_NAMESPACE:-trace-lab}"
ghcr_pull_secret_name="${GHCR_PULL_SECRET_NAME:-}"
ghcr_username="${GHCR_USERNAME:-}"
ghcr_token="${GHCR_TOKEN:-}"

if [[ -z "${ghcr_namespace}" ]]; then
  echo "error: GHCR_NAMESPACE must be set, for example ghcr.io/your-user-or-org." >&2
  exit 1
fi

if [[ "${ghcr_namespace}" != ghcr.io/* ]]; then
  echo "error: GHCR_NAMESPACE must start with ghcr.io/." >&2
  exit 1
fi

if [[ -z "${image_tag}" ]]; then
  echo "error: IMAGE_TAG must be set so the remote deploy can reference the published app images." >&2
  exit 1
fi

if [[ -z "${base_domain}" ]]; then
  echo "error: TRACE_LAB_BASE_DOMAIN must be set, for example 203.0.113.10.sslip.io or lab.example.com." >&2
  exit 1
fi

if [[ -n "${ghcr_pull_secret_name}" ]] && ([[ -z "${ghcr_username}" ]] || [[ -z "${ghcr_token}" ]]); then
  echo "error: GHCR_USERNAME and GHCR_TOKEN are required when GHCR_PULL_SECRET_NAME is set." >&2
  exit 1
fi

coach_host="coach.${base_domain}"
shop_host="shop.${base_domain}"
jaeger_host="jaeger.${base_domain}"
jaeger_url="http://${jaeger_host}"

tmpdir="$(mktemp -d "${repo_root}/.tmp.cloudtracing-remote.XXXXXX")"
trap 'rm -rf "${tmpdir}"' EXIT

if [[ -n "${ghcr_pull_secret_name}" ]]; then
  kubectl get namespace "${namespace}" >/dev/null 2>&1 || kubectl create namespace "${namespace}" >/dev/null
  kubectl -n "${namespace}" create secret docker-registry "${ghcr_pull_secret_name}" \
    --docker-server=ghcr.io \
    --docker-username="${ghcr_username}" \
    --docker-password="${ghcr_token}" \
    --dry-run=client -o yaml | kubectl apply -f -

  cat >"${tmpdir}/default-service-account.yaml" <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: default
imagePullSecrets:
  - name: ${ghcr_pull_secret_name}
EOF
fi

cat >"${tmpdir}/coach-jaeger-url.yaml" <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: coach
spec:
  template:
    spec:
      containers:
        - name: coach
          env:
            - name: JAEGER_UI_URL
              value: ${jaeger_url}
EOF

cat >"${tmpdir}/remote-ingress.yaml" <<EOF
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: trace-lab
spec:
  ingressClassName: traefik
  rules:
    - host: ${shop_host}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: shop-web
                port:
                  number: 8080
    - host: ${coach_host}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: coach
                port:
                  number: 8080
    - host: ${jaeger_host}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: jaeger
                port:
                  number: 16686
EOF

{
  cat <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

namespace: ${namespace}

resources:
  - ../k8s/base
  - remote-ingress.yaml
EOF

  if [[ -n "${ghcr_pull_secret_name}" ]]; then
    cat <<EOF
  - default-service-account.yaml
EOF
  fi

  cat <<EOF

images:
  - name: cloudtracing/catalog
    newName: ${ghcr_namespace}/cloudtracing/catalog
    newTag: "${image_tag}"
  - name: cloudtracing/inventory
    newName: ${ghcr_namespace}/cloudtracing/inventory
    newTag: "${image_tag}"
  - name: cloudtracing/orders
    newName: ${ghcr_namespace}/cloudtracing/orders
    newTag: "${image_tag}"
  - name: cloudtracing/payments
    newName: ${ghcr_namespace}/cloudtracing/payments
    newTag: "${image_tag}"
  - name: cloudtracing/edge
    newName: ${ghcr_namespace}/cloudtracing/edge
    newTag: "${image_tag}"
  - name: cloudtracing/shop-web
    newName: ${ghcr_namespace}/cloudtracing/shop-web
    newTag: "${image_tag}"
  - name: cloudtracing/coach
    newName: ${ghcr_namespace}/cloudtracing/coach
    newTag: "${image_tag}"
  - name: cloudtracing/jaeger-ui
    newName: ${ghcr_namespace}/cloudtracing/jaeger-ui
    newTag: "${image_tag}"
EOF

  if [[ "${mirror_upstream}" != "0" ]]; then
    cat <<EOF
  - name: ${POSTGRES_IMAGE_REPO}
    newName: ${ghcr_namespace}/cloudtracing-third-party/postgres
    newTag: "${POSTGRES_IMAGE_TAG}"
  - name: ${REDIS_IMAGE_REPO}
    newName: ${ghcr_namespace}/cloudtracing-third-party/redis
    newTag: "${REDIS_IMAGE_TAG}"
  - name: ${MEILISEARCH_IMAGE_REPO}
    newName: ${ghcr_namespace}/cloudtracing-third-party/meilisearch
    newTag: "${MEILISEARCH_IMAGE_TAG}"
  - name: ${JAEGER_IMAGE_REPO}
    newName: ${ghcr_namespace}/cloudtracing-third-party/jaeger
    newTag: "${JAEGER_VERSION}"
EOF
  fi

  cat <<EOF

patches:
  - path: ../k8s/overlays/local/image-pull-policy.yaml
  - path: coach-jaeger-url.yaml
EOF
} >"${tmpdir}/kustomization.yaml"

echo "Deploying remote overlay for ${coach_host}, ${shop_host}, and ${jaeger_host}..."
APPS= KUSTOMIZE_DIR="${tmpdir}" TRACE_LAB_NAMESPACE="${namespace}" bash "${repo_root}/scripts/deploy.sh"

echo
echo "Cluster is ready:"
echo "  http://${coach_host}"
echo "  http://${shop_host}"
echo "  http://${jaeger_host}"
