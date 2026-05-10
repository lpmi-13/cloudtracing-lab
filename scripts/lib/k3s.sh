#!/usr/bin/env bash
# Detect and repair k3s node-IP drift.
#
# k3s is configured with `node-ip` / `advertise-address` in /etc/rancher/k3s/config.yaml.
# If the host's primary IP changes (different LAN, VPN on/off, DHCP renewal), k3s
# refuses to start because the configured IP is no longer on any interface, and
# systemd loops it forever. This module detects the drift and rewrites the config.

run_privileged() {
  if [[ "$(id -u)" -eq 0 ]]; then
    "$@"
  else
    sudo "$@"
  fi
}

current_host_ip() {
  ip route get 1 2>/dev/null | awk '{print $7; exit}'
}

k3s_node_internal_ip() {
  local node_name="$1"
  kubectl get node "${node_name}" \
    -o jsonpath='{range .status.addresses[?(@.type=="InternalIP")]}{.address}{end}' 2>/dev/null || true
}

kubernetes_endpoint_ip() {
  local ip
  ip="$(kubectl get endpointslices.discovery.k8s.io -n default \
    -l kubernetes.io/service-name=kubernetes \
    -o jsonpath='{.items[0].endpoints[0].addresses[0]}' 2>/dev/null || true)"

  if [[ -z "${ip}" ]]; then
    ip="$(kubectl get endpoints kubernetes -n default \
      -o jsonpath='{.subsets[0].addresses[0].ip}' 2>/dev/null || true)"
  fi

  echo "${ip}"
}

update_k3s_ip_config() {
  local desired_ip="$1"
  local config_file="/etc/rancher/k3s/config.yaml"
  local stripped next timestamp
  stripped="$(mktemp)"
  next="$(mktemp)"
  timestamp="$(date +%Y%m%d%H%M%S)"

  if run_privileged test -f "${config_file}"; then
    run_privileged awk '!/^(node-ip|advertise-address):[[:space:]]/' "${config_file}" > "${stripped}"
    run_privileged cp "${config_file}" "${config_file}.bak.${timestamp}"
  else
    : > "${stripped}"
    run_privileged mkdir -p "$(dirname "${config_file}")"
  fi

  cat "${stripped}" > "${next}"
  if [[ -s "${next}" ]]; then
    printf '\n' >> "${next}"
  fi
  printf 'node-ip: %s\n' "${desired_ip}" >> "${next}"
  printf 'advertise-address: %s\n' "${desired_ip}" >> "${next}"

  run_privileged install -m 0644 "${next}" "${config_file}"
  rm -f "${stripped}" "${next}"
}

wait_for_k3s_api() {
  local waited=0
  while ! kubectl cluster-info >/dev/null 2>&1; do
    if [[ ${waited} -ge 180 ]]; then
      echo "  ERROR: k3s API did not become ready after restart" >&2
      return 1
    fi
    sleep 3
    waited=$((waited + 3))
  done
}

wait_for_k3s_advertised_ip() {
  local node_name="$1"
  local desired_ip="$2"
  local waited=0 node_ip endpoint_ip

  while [[ ${waited} -lt 180 ]]; do
    node_ip="$(k3s_node_internal_ip "${node_name}")"
    endpoint_ip="$(kubernetes_endpoint_ip)"

    if [[ "${node_ip}" == "${desired_ip}" && "${endpoint_ip}" == "${desired_ip}" ]]; then
      return 0
    fi

    sleep 3
    waited=$((waited + 3))
  done

  echo "  ERROR: k3s still advertises node IP '${node_ip:-unknown}' and API endpoint '${endpoint_ip:-unknown}'" >&2
  echo "         Expected both to be '${desired_ip}'" >&2
  return 1
}

# After a node-IP rewrite the persisted Node object still carries the old
# InternalIP. The kube-router network-policy controller reads it, can't find a
# matching interface, and shuts k3s down — kicking off an auto-restart loop
# that prevents the kubelet from ever updating the Node status. Deleting the
# Node lets the kubelet re-register it with the new --node-ip.
force_node_reregister_if_stale() {
  local node_name="$1"
  local desired_ip="$2"
  local waited=0 ip

  while [[ ${waited} -lt 60 ]]; do
    if timeout 5 kubectl delete node "${node_name}" --force --grace-period=0 --ignore-not-found >/dev/null 2>&1; then
      break
    fi
    sleep 2
    waited=$((waited + 2))
  done

  waited=0
  while [[ ${waited} -lt 180 ]]; do
    ip="$(k3s_node_internal_ip "${node_name}")"
    if [[ "${ip}" == "${desired_ip}" ]]; then
      return 0
    fi
    sleep 2
    waited=$((waited + 2))
  done

  echo "  ERROR: node ${node_name} did not re-register with InternalIP ${desired_ip}" >&2
  return 1
}

# Detect a mismatch between the host's current primary IP and what k3s advertises,
# and rewrite /etc/rancher/k3s/config.yaml + restart k3s if needed.
repair_k3s_node_ip_if_needed() {
  local desired_ip node_name node_ip endpoint_ip

  desired_ip="$(current_host_ip)"
  if [[ -z "${desired_ip}" || "${desired_ip}" == 127.* ]]; then
    echo "  ERROR: could not determine a non-loopback host IP for k3s" >&2
    return 1
  fi

  if ! kubectl cluster-info >/dev/null 2>&1; then
    # API isn't reachable. Most likely k3s is in the restart loop caused by a
    # stale node-ip. Force a config rewrite + restart and let the waits below
    # confirm recovery.
    echo "  k3s API unreachable; assuming node-IP drift and rewriting config to ${desired_ip}..."
    update_k3s_ip_config "${desired_ip}"
    run_privileged systemctl restart k3s
    wait_for_k3s_api
  fi

  node_name="$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  if [[ -z "${node_name}" ]]; then
    echo "  ERROR: could not determine k3s node name" >&2
    return 1
  fi

  node_ip="$(k3s_node_internal_ip "${node_name}")"
  endpoint_ip="$(kubernetes_endpoint_ip)"

  if [[ "${node_ip}" == "${desired_ip}" && "${endpoint_ip}" == "${desired_ip}" ]]; then
    echo "  OK: k3s advertises current host IP (${desired_ip})"
    return 0
  fi

  echo "  Detected k3s IP drift:"
  echo "    current host IP:         ${desired_ip}"
  echo "    k3s node InternalIP:     ${node_ip:-unknown}"
  echo "    Kubernetes API endpoint: ${endpoint_ip:-unknown}"
  echo "  Updating /etc/rancher/k3s/config.yaml and restarting k3s..."

  update_k3s_ip_config "${desired_ip}"
  run_privileged systemctl restart k3s

  wait_for_k3s_api
  force_node_reregister_if_stale "${node_name}" "${desired_ip}"
  kubectl wait --for=condition=Ready --timeout=180s "node/${node_name}" >/dev/null
  wait_for_k3s_advertised_ip "${node_name}" "${desired_ip}"
  echo "  OK: k3s now advertises ${desired_ip}"
}
