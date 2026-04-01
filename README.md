# Cloud Tracing Lab

This repo scaffolds a deliberate-practice activity for trace-driven incident diagnosis in a realistic local `k3s` environment.

The initial MVP is built around:

- direct `localhost` ports for local runs and ingress for generic remote clusters
- a Python web tier
- mixed Go and Python application services
- PostgreSQL, Redis, and Meilisearch as the data tier
- OpenTelemetry for trace generation
- Jaeger for trace exploration
- a coach UI that generates traffic, explains the objective, grades learner answers, and advances to the next randomized scenario after a correct diagnosis

## Learning Design

The activity is grounded in the deliberate-practice principles summarized in `/home/adam/projects/deliberate-practice`:

- the skill stays constant: identify the true root-cause component from traces
- the scenario varies: search, checkout, payment, and order-history flows fail in different ways
- feedback is immediate: the coach UI grades the suspected service and issue type
- replay is built in: learners can rotate to a new scenario and repeat the same workflow

The MVP focuses on activity-level deliberate practice. Flame graphs are left as a later extension.

## Architecture

`localhost ports or remote ingress -> coach + shop-web (Python); shop-web -> edge-api (Go) -> catalog/inventory/orders (Go) + payments (Python) -> PostgreSQL / Redis / Meilisearch`

Supporting services:

- `jaeger`
- `coach` for learner guidance, grading, and randomized scenario progression

## Current State

- the learner loop is end to end: the coach picks a scenario, seeds fresh traffic automatically, grades answers, and advances after a correct diagnosis
- the coach UI now includes lightweight progressive hints that help learners move from the entry span to the next service layer without revealing the answer
- the application tier covers four trace patterns: broad search queries, inventory N+1 work, payment lock waits, and expensive order-history sorts
- the optional shop UI remains available for manual storefront traffic, but the core activity is now coach + Jaeger
- the local path is `bash scripts/up.sh`, which tears the lab down, rebuilds local `:latest` images, pushes them to the trusted registry, applies the local overlay, and waits for rollout
- the remote path is `bash scripts/publish-ghcr.sh` plus `bash scripts/deploy-remote.sh`, or simply `bash scripts/up-remote.sh`
- the preloaded VM/rootfs path is still available for iximiuz-style playgrounds and fast-start environments

## Scenario Types

The scenario catalog lives in [scenarios/scenarios.json](/home/adam/projects/cloudtracing/scenarios/scenarios.json).

Initial faults:

- slow search caused by a broad Meilisearch query after a Redis miss
- slow checkout caused by inventory N+1 queries
- failing checkout caused by a payment lock-wait timeout
- slow account history caused by an expensive order-history sort

## Learner Loop

1. The coach UI picks a scenario at random and automatically seeds five fresh traces into every learner-facing endpoint.
2. The learner opens Jaeger, filters to the focus service and operation from the coach, and inspects the newest matching trace.
3. The learner submits the suspected culprit service and issue type in the coach UI.
4. If the diagnosis is wrong, the current scenario stays in place so the learner can keep inspecting the latest trace or start a new scenario manually.
5. If the diagnosis is correct, the coach UI immediately advances to a new random scenario and seeds the next batch automatically.
6. The loop continues until the learner decides to stop.

## Repo Layout

- `cmd/coach`: learner UI and grader
- `cmd/edge`: entry application tier API
- `services/catalog`: Go catalog service
- `services/inventory`: Go inventory service
- `services/orders`: Go orders service
- `python/web`: Python web tier
- `python/payments`: Python payments service
- `db/init`: PostgreSQL schema and seed data
- `k8s/base`: Kubernetes manifests
- `pkg/telemetry`: shared Go OpenTelemetry setup
- `python/common`: shared Python telemetry and scenario helpers

## Script Guide

Use this as the quick "which script do I run?" reference:

| Script | Run it when | What it does and why it exists |
| --- | --- | --- |
| `bash scripts/up.sh` | You want the normal one-command local bring-up. | Runs the full local flow: build app images, push them to the trusted local registry, deploy the local overlay, bind the local HTTP services to `localhost`, and wait for rollout. This is the default local entry point. |
| `bash scripts/refresh-local-app.sh coach` | You changed one or a few app images and want the fastest local refresh without tearing the namespace down. | Rebuilds only the selected app images, pushes only those tags to the local registry, and restarts only the matching deployments. This exists for quick iteration on things like the coach UI. |
| `bash scripts/down.sh` | You want to remove the local lab before a clean retry or before leaving the cluster idle. | Deletes the `trace-lab` namespace and waits for it to disappear, so the next local run starts from a clean slate. |
| `bash scripts/build-images.sh` | You changed app code or Dockerfiles and need fresh first-party images. | Builds the Go and Python app images as `cloudtracing/*:${IMAGE_TAG:-latest}` for the normal local flow. This exists so image creation is consistent across local, remote, and rootfs flows. |
| `bash scripts/load-images.sh` | You built local images and want `k3s` to be able to pull them. | Starts the local registry on `localhost:30300` if needed, then tags and pushes the app images there. We need it because local `k3s` does not read images directly from the host Docker daemon. |
| `bash scripts/deploy.sh` | You changed Kubernetes manifests, changed overlays, or loaded fresh images and want them live. | Applies the selected kustomize overlay, removes legacy OpenSearch resources, restarts app deployments, and waits for rollout. We need it because a same-tag image update like `:latest` does not reliably refresh pods on `kubectl apply` alone. |
| `bash scripts/publish-ghcr.sh` | You want to run the lab on a remote cluster that pulls from GHCR. | Publishes the first-party app images to `ghcr.io/.../cloudtracing/*:${IMAGE_TAG}` and optionally mirrors third-party runtime images too. If `IMAGE_TAG` is unset, it prompts for a tag and suggests the next `vN` based on the current GHCR app-image tags. |
| `bash scripts/deploy-remote.sh` | You already published images and want to deploy them to a remote cluster with host-based ingress. | Renders a temporary remote overlay with GHCR image rewrites, ingress hosts, optional pull secret wiring, and the remote Jaeger URL for the coach, then deploys it. Set `IMAGE_TAG` to the same tag you just published. |
| `bash scripts/up-remote.sh` | You want the full GHCR-based remote flow in one command. | Runs `publish-ghcr.sh` and then `deploy-remote.sh`, carrying the chosen publish tag into the deploy step automatically. This is the shortest path for the generic remote-cluster workflow. |
| `bash scripts/build-rootfs-image.sh` | You are preparing a fast-start VM or playground image and want the whole stack preloaded. | Builds a rootfs image containing the manifests plus all required container images. We need it for environments where boot speed matters more than pulling from a registry on first start. |
| `bash scripts/deploy-preloaded-vm.sh` | You are inside the preloaded VM and want to deploy or redeploy the fixed-port VM overlay. | Deploys the VM overlay that binds coach, shop, and Jaeger directly on stable VM host ports. This exists because the VM path is exposed-port-based rather than host-based ingress. |

## Local Run Commands

Preferred one-command local bring-up:

```bash
bash scripts/up.sh
```

That command:

- deletes the existing `trace-lab` namespace first so every local run starts clean
- builds the application images with Docker
- pushes them to the local registry on `localhost:30300`
- applies the local `k3s` manifests
- restarts the application deployments so refreshed local `:latest` images are re-pulled
- waits for the `trace-lab` deployments to finish rolling out

If you want to run the steps manually, use:

```bash
bash scripts/build-images.sh
bash scripts/load-images.sh
bash scripts/deploy.sh
```

If you only changed one app and the cluster is already up, use the faster targeted path instead of rebuilding everything:

```bash
bash scripts/refresh-local-app.sh coach
```

You can pass more than one app name, for example:

```bash
bash scripts/refresh-local-app.sh coach shop-web
```

After the deploy completes, open:

- `http://localhost:9000` for the coach UI
- `http://localhost:9002` for Jaeger

Optional manual storefront/debug surface:

- `http://localhost:9001` for the shop UI

Note: this machine's `k3s` setup already trusts a local registry at `localhost:30300`. `scripts/load-images.sh` starts that registry if needed and pushes the app images there, so the normal local build/load/deploy flow does not require `sudo`.

The local overlay also binds the internal HTTP services to fixed loopback ports for direct debugging:

- `http://localhost:9003` for `edge-api`
- `http://localhost:9004` for `catalog-api`
- `http://localhost:9005` for `inventory-api`
- `http://localhost:9006` for `orders-api`
- `http://localhost:9007` for `payments-api`
- `http://localhost:9008` for `meilisearch`

## Start The Investigation

1. Open `http://localhost:9000` first and read the active scenario title, objective, and route. The coach seeds five fresh traces across all learner-facing endpoints as soon as the scenario loads.
2. Open `http://localhost:9002`, set Jaeger to the focus service and operation shown in the coach, and inspect the newest matching trace.
3. Start at the web tier span, then follow the request downstream through `edge-api` into the backing service spans.
4. Identify the component creating the real slowdown or failure, not just the first upstream service that noticed it. Pay close attention to long database, Redis, or Meilisearch spans.
5. If you get stuck moving from the entry span into the real suspect branch, use the `Need a hint?` panel in the coach UI for a minimal nudge.
6. Go back to the coach UI and submit the culprit service plus issue type. If you are wrong, the current scenario stays in place; if you want a fresh batch, click `New Scenario`.
7. Repeat until you solve it or move to a new scenario.

Jaeger now keeps only the most recent activity-sized batch in memory. The local deployment caps in-memory storage at 15 traces, so each 5-per-endpoint batch replaces the previous one without waiting for Jaeger to restart.

If you want to explore the storefront manually alongside the guided activity, `http://localhost:9001` still exposes search, checkout, and account-history flows, but the coach no longer depends on manual trace generation.

Reloading the coach page does not rotate the activity. The active scenario only changes when the backend advances to the next activity after a correct answer or when you click `New Scenario`.

## Remote VM Workflow

The remote deployment flow follows the same shape as `/home/adam/projects/vulnerable-k8s-operator`: publish images to a registry you control, then deploy manifests that point at those published tags.

For this repo there are multiple first-party services, plus optional runtime dependencies to mirror, so the equivalent commands are:

```bash
export GHCR_NAMESPACE=ghcr.io/<your-user-or-org>
export TRACE_LAB_BASE_DOMAIN=<public-ip>.sslip.io
export IMAGE_TAG=v5

bash scripts/publish-ghcr.sh
bash scripts/deploy-remote.sh
```

Or as a single step:

```bash
export GHCR_NAMESPACE=ghcr.io/<your-user-or-org>
export TRACE_LAB_BASE_DOMAIN=<public-ip>.sslip.io

bash scripts/up-remote.sh
```

That remote path:

- publishes the application images to `ghcr.io/<your-user-or-org>/cloudtracing/*:<IMAGE_TAG>`
- mirrors the third-party runtime images into `ghcr.io/<your-user-or-org>/cloudtracing-third-party/*` by default
- renders a temporary remote kustomize overlay with `coach.<domain>`, `shop.<domain>`, and `jaeger.<domain>` ingress hosts
- points the coach UI at the remote Jaeger URL
- applies the manifests and waits for the rollouts

Notes:

- Run `docker login ghcr.io` before publishing.
- If `IMAGE_TAG` is unset, `scripts/publish-ghcr.sh` prompts for a tag and suggests the next `vN` found across the existing GHCR app images.
- If you run `scripts/deploy-remote.sh` separately after publishing, export the same chosen `IMAGE_TAG` first. `scripts/up-remote.sh` handles that handoff for you automatically.
- `TRACE_LAB_BASE_DOMAIN` is only needed for the generic host-based ingress flow in `scripts/deploy-remote.sh`.
- The remote cluster should have Traefik or another ingress controller available at the `traefik` ingress class.
- If you want the cluster to keep pulling upstream images directly instead of mirroring them into GHCR, set `MIRROR_UPSTREAM_IMAGES=0` for both scripts.
- If your GHCR packages are private, set `GHCR_PULL_SECRET_NAME`, `GHCR_USERNAME`, and `GHCR_TOKEN` before running `bash scripts/deploy-remote.sh`. The script will create a pull secret in `trace-lab` and attach it to the default service account.

For iximiuz-style VMs where each learner-facing UI gets its own exposed port and iximiuz supplies the public hostname, skip this host-based ingress flow and use the fast-start rootfs path below.

## Fast-Start Rootfs Image

For VM environments where cold-start speed matters more than registry reuse, there is now a separate rootfs image flow modeled after the iximiuz playground setup in `/home/adam/projects/iximiuz-playgrounds/owasp-k3s-cluster`.

That path bakes the lab into a k3s-capable filesystem image by:

- building the first-party app images locally
- pulling the runtime dependency images locally
- saving all Kubernetes images into a single archive
- copying the lab manifests into the rootfs image
- enabling a systemd bootstrap unit that imports the images into k3s containerd and deploys the lab on boot
- exposing the main learner-facing UIs plus the optional storefront debug UI on fixed VM host ports:
  - coach on `30080`
  - jaeger on `30686`
  - shop on `30081` for manual storefront traffic if you need it

Build it with:

```bash
export ROOTFS_IMAGE=ghcr.io/<your-user-or-org>/cloudtracing-k3s-rootfs:v1

bash scripts/build-rootfs-image.sh
docker push "${ROOTFS_IMAGE}"
```

The rootfs build uses [playground/iximiuz/Dockerfile](/home/adam/projects/cloudtracing/playground/iximiuz/Dockerfile), and the bootstrap unit is [trace-lab-bootstrap.service](/home/adam/projects/cloudtracing/playground/iximiuz/image/trace-lab-bootstrap.service).

Inside the VM, the bootstrap script deploys the dedicated [vm overlay](/home/adam/projects/cloudtracing/k8s/overlays/vm/kustomization.yaml), which binds coach, Jaeger, and the optional shop debug UI directly on stable VM host ports. This avoids relying on Kubernetes `NodePort` behavior for browser-driven playground port tabs. From the VM itself, the endpoints are:

```bash
http://127.0.0.1:30080
http://127.0.0.1:30081
http://127.0.0.1:30686
```

For iximiuz, expose coach and Jaeger publicly in the playground manifest, and only expose shop if you want manual storefront debugging available to learners. A starter manifest is included at [playground/iximiuz/manifest.yaml](/home/adam/projects/cloudtracing/playground/iximiuz/manifest.yaml); update the OCI image reference before using it.

The bootstrap script lives at [bootstrap-trace-lab.sh](/home/adam/projects/cloudtracing/playground/iximiuz/image/bootstrap-trace-lab.sh), and the in-VM deploy path it calls is [deploy-preloaded-vm.sh](/home/adam/projects/cloudtracing/scripts/deploy-preloaded-vm.sh).
