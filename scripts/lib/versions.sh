#!/usr/bin/env bash

readonly DEFAULT_FIRST_PARTY_IMAGE_TAG="v5"
readonly DEFAULT_IXIMIUZ_ROOTFS_IMAGE="ghcr.io/lpmi-13/cloudtracing-k3s-rootfs:v5"

readonly POSTGRES_IMAGE_REPO="postgres"
readonly POSTGRES_IMAGE_TAG="17.4-alpine"
readonly POSTGRES_IMAGE="${POSTGRES_IMAGE_REPO}:${POSTGRES_IMAGE_TAG}"

readonly REDIS_IMAGE_REPO="redis"
readonly REDIS_IMAGE_TAG="8.4.0-alpine"
readonly REDIS_IMAGE="${REDIS_IMAGE_REPO}:${REDIS_IMAGE_TAG}"

readonly MEILISEARCH_IMAGE_REPO="getmeili/meilisearch"
readonly MEILISEARCH_IMAGE_TAG="v1.15"
readonly MEILISEARCH_IMAGE="${MEILISEARCH_IMAGE_REPO}:${MEILISEARCH_IMAGE_TAG}"

# Jaeger release v2.17.0 is tagged at jaegertracing/jaeger commit
# b5be18b10053a087a9bb272a1d9a0c71bcaac2c7.
readonly JAEGER_VERSION="2.17.0"
readonly JAEGER_GIT_TAG="v${JAEGER_VERSION}"
readonly JAEGER_COMMIT_SHA="b5be18b10053a087a9bb272a1d9a0c71bcaac2c7"
readonly JAEGER_IMAGE_REPO="cr.jaegertracing.io/jaegertracing/jaeger"
readonly JAEGER_IMAGE="${JAEGER_IMAGE_REPO}:${JAEGER_VERSION}"

# Jaeger UI main HEAD as of 2026-04-03 resolves to commit
# fc70e816ce97da9a29ded7808bf5c5e7239beb4b.
readonly JAEGER_UI_VERSION="main-2026-04-03-fc70e81"
readonly JAEGER_UI_GIT_TAG="main"
readonly JAEGER_UI_COMMIT_SHA="fc70e816ce97da9a29ded7808bf5c5e7239beb4b"
readonly JAEGER_UI_IMAGE_REPO="cloudtracing/jaeger-ui"

# Jaeger v2 memory search requires search depth to be strictly less than
# memory.max_traces. The lab keeps 15 traces per activity batch, so the UI must
# cap search results below that.
readonly JAEGER_UI_SEARCH_MAX_LIMIT="14"
